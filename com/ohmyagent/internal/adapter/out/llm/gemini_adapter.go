package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"

	"google.golang.org/genai"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*GeminiAdapter)(nil)

const (
	// defaultGeminiModel 은 req.Model / config.Model 이 모두 비었을 때의 기본 모델이다.
	defaultGeminiModel = "gemini-1.5-flash"
)

// GeminiAdapter 는 공식 Google Gen AI Go SDK(google.golang.org/genai)를 사용하는
// Gemini API 어댑터다. 스트리밍 생성과 function-calling 을 지원한다.
//
// 주의: 클라이언트는 ChatStream 호출마다 생성한다. API 키는 환경변수에서 매 호출 시 읽으므로
// 어댑터 인스턴스는 키 값을 보관하지 않는다(보안 + 키 회전 대응).
type GeminiAdapter struct {
	endpoint   string // 현재 Gemini Developer 백엔드에서는 사용하지 않음(미래 확장/문서화 목적 보존)
	model      string
	apiKey     string // 직접 저장된 키(복호화된 평문). 비면 apiKeyEnv 환경변수 사용
	apiKeyEnv  string
	httpClient *http.Client // 공유 커넥션 풀(factory 가 주입)
}

// NewGeminiAdapter 는 도메인 ProviderConfig 로부터 GeminiAdapter 를 생성한다.
// 스트리밍은 장시간 지속될 수 있으므로 타임아웃은 두지 않고 ctx 로 취소를 제어한다.
func NewGeminiAdapter(config domainllmprovider.ProviderConfig, httpClient *http.Client) *GeminiAdapter {
	return &GeminiAdapter{
		endpoint:   config.Endpoint,
		model:      config.Model,
		apiKey:     config.APIKey,
		apiKeyEnv:  config.APIKeyEnv,
		httpClient: httpClient,
	}
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *GeminiAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

// resolveGeminiModel 은 req.Model → a.model → 기본값 순으로 사용할 모델명을 고른다.
func (a *GeminiAdapter) resolveGeminiModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultGeminiModel
}

// ChatStream 은 Gemini GenerateContentStream 을 호출하고 응답 조각을 onChunk 로 전달한다.
// 텍스트 델타는 도착하는 즉시 흘려보내고, FunctionCall 파트는 누적했다가 마지막 Done 조각에 담는다.
//
// onChunk 가 에러를 반환하면 즉시 스트리밍을 중단하고 그 에러를 그대로 반환한다(마지막 Done 조각도 보내지 않음).
func (a *GeminiAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := resolveAPIKey(a.apiKey, a.apiKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("gemini: %w: API key not set (set config api_key or api_key_env)", domainllmprovider.ErrUpstream)
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: a.httpClient, // 공유 커넥션 풀
	})
	if err != nil {
		return fmt.Errorf("gemini: %w: create client: %v", domainllmprovider.ErrUpstream, err)
	}

	contents, systemInstruction := geminiBuildContents(req.Messages)

	config := &genai.GenerateContentConfig{}
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}
	if req.MaxTokens > 0 {
		// SDK 의 MaxOutputTokens 는 plain int32 다(*int32 아님).
		config.MaxOutputTokens = int32(req.MaxTokens)
	}
	if req.Temperature != nil {
		// 도메인은 *float64, SDK 는 *float32 를 기대하므로 변환해 포인터로 넘긴다.
		t := float32(*req.Temperature)
		config.Temperature = genai.Ptr(t)
	}
	tools, err := geminiBuildTools(req.Tools)
	if err != nil {
		return err
	}
	if len(tools) > 0 {
		config.Tools = tools
	}

	var (
		finishReason  string
		usage         *domainllmprovider.ChatUsage
		collectedCall []domainllmprovider.ToolCall
		callIndex     int
	)

	model := a.resolveGeminiModel(req.Model)

	// Go 1.23 iterator: iter.Seq2[*GenerateContentResponse, error].
	for resp, streamErr := range client.Models.GenerateContentStream(ctx, model, contents, config) {
		if streamErr != nil {
			return fmt.Errorf("gemini: %w: %v", domainllmprovider.ErrUpstream, streamErr)
		}
		// ctx 취소를 명시적으로 존중(SDK 가 즉시 끊지 않는 경우 대비).
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("gemini: %w: %v", domainllmprovider.ErrUpstream, err)
		}
		if resp == nil {
			continue
		}

		// 사용량/완료사유는 매 조각에서 최신값으로 갱신한다(마지막 조각이 최종값).
		if resp.UsageMetadata != nil {
			usage = &domainllmprovider.ChatUsage{
				PromptTokens:     int(resp.UsageMetadata.PromptTokenCount),
				CompletionTokens: int(resp.UsageMetadata.CandidatesTokenCount),
				TotalTokens:      int(resp.UsageMetadata.TotalTokenCount),
			}
		}

		for _, cand := range resp.Candidates {
			if cand == nil {
				continue
			}
			if cand.FinishReason != "" {
				// FinishReason 은 genai.FinishReason(string) 원문 그대로 보존("STOP"/"MAX_TOKENS" 등).
				finishReason = string(cand.FinishReason)
			}
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				if part == nil {
					continue
				}
				// 텍스트 델타: 도착 즉시 흘려보낸다.
				if part.Text != "" {
					if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: part.Text}); err != nil {
						return err
					}
				}
				// FunctionCall: 마지막 Done 조각에 담기 위해 누적.
				if part.FunctionCall != nil {
					tc, convErr := geminiToToolCall(part.FunctionCall, callIndex)
					if convErr != nil {
						return convErr
					}
					collectedCall = append(collectedCall, tc)
					callIndex++
				}
			}
		}
	}

	// 스트림 종료 후 정확히 1회 최종 조각 전송.
	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    collectedCall,
		Usage:        usage,
	})
}

// geminiBuildContents 는 도메인 메시지를 Gemini Content 슬라이스로 변환하고,
// system 메시지를 모아 SystemInstruction(Content)으로 분리해 반환한다.
//
// 역할 매핑:
//   - system    → SystemInstruction 의 텍스트 파트로 누적(개행으로 join)
//   - user      → role "user" + 텍스트 파트
//   - assistant → role "model" + 텍스트 파트, ToolCalls 가 있으면 FunctionCall 파트(Name + Args)
//   - tool      → role "user" + FunctionResponse 파트(함수 결과). Gemini 는 별도 "tool" role 이 없고
//     function-response 파트를 user 턴으로 보낸다.
//
// 도구 결과의 함수명(Name) 결정 순서: message.Name → ToolCallID 로 직전 assistant ToolCalls 에서 역매핑 →
// 둘 다 없으면 빈 문자열(SDK 가 거부할 수 있으나 도메인 입력 문제이므로 그대로 전달).
func geminiBuildContents(msgs []domainllmprovider.ChatMessage) (contents []*genai.Content, systemInstruction *genai.Content) {
	var systemParts []string

	// ToolCallID → 함수명 역매핑 테이블(assistant 가 만든 호출에서 수집).
	callIDToName := map[string]string{}

	for _, m := range msgs {
		switch m.Role {
		case domainllmprovider.ChatRoleSystem:
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}

		case domainllmprovider.ChatRoleUser:
			contents = append(contents, &genai.Content{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{genai.NewPartFromText(m.Content)},
			})

		case domainllmprovider.ChatRoleAssistant:
			var parts []*genai.Part
			if m.Content != "" {
				parts = append(parts, genai.NewPartFromText(m.Content))
			}
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && tc.Name != "" {
					callIDToName[tc.ID] = tc.Name
				}
				args := geminiDecodeArgs(tc.Arguments)
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: args,
					},
				})
			}
			if len(parts) == 0 {
				// 빈 assistant 메시지는 건너뛴다(Gemini 는 빈 Content 를 거부할 수 있음).
				continue
			}
			contents = append(contents, &genai.Content{
				Role:  genai.RoleModel,
				Parts: parts,
			})

		case domainllmprovider.ChatRoleTool:
			name := m.Name
			if name == "" {
				name = callIDToName[m.ToolCallID]
			}
			contents = append(contents, &genai.Content{
				Role: genai.RoleUser,
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       m.ToolCallID,
						Name:     name,
						Response: geminiBuildToolResponse(m.Content),
					},
				}},
			})

		default:
			// 알 수 없는 역할은 user 텍스트로 베스트에포트 처리.
			contents = append(contents, &genai.Content{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{genai.NewPartFromText(m.Content)},
			})
		}
	}

	if len(systemParts) > 0 {
		systemInstruction = &genai.Content{
			Parts: []*genai.Part{genai.NewPartFromText(strings.Join(systemParts, "\n"))},
		}
	}
	return contents, systemInstruction
}

// geminiDecodeArgs 는 도구 호출 Arguments(JSON 문자열)를 map[string]any 로 디코드한다.
// 비었거나 파싱 실패 시 빈 맵을 반환한다(SDK Args 는 nil 보다 빈 맵이 안전).
func geminiDecodeArgs(arguments string) map[string]any {
	args := map[string]any{}
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return args
	}
	_ = json.Unmarshal([]byte(trimmed), &args)
	return args
}

// geminiBuildToolResponse 는 도구 실행 결과 문자열을 FunctionResponse.Response(map) 로 만든다.
// Content 가 JSON object 이면 그대로 사용하고, 아니면 {"result": <content>} 로 감싼다.
func geminiBuildToolResponse(content string) map[string]any {
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			return obj
		}
	}
	return map[string]any{"result": content}
}

// geminiToToolCall 은 SDK FunctionCall 을 도메인 ToolCall 로 변환한다.
// ID 가 비면 인덱스로 합성한다("call_<index>"). Arguments 는 Args 맵을 JSON 문자열로 직렬화한다.
func geminiToToolCall(fc *genai.FunctionCall, index int) (domainllmprovider.ToolCall, error) {
	id := fc.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", index)
	}
	var arguments string
	if len(fc.Args) > 0 {
		b, err := json.Marshal(fc.Args)
		if err != nil {
			return domainllmprovider.ToolCall{}, fmt.Errorf("gemini: %w: marshal function args: %v", domainllmprovider.ErrUpstream, err)
		}
		arguments = string(b)
	} else {
		arguments = "{}"
	}
	return domainllmprovider.ToolCall{
		ID:        id,
		Name:      fc.Name,
		Arguments: arguments,
	}, nil
}

// geminiBuildTools 는 도메인 ToolDefinition 들을 단일 genai.Tool(FunctionDeclarations 묶음)로 변환한다.
// Parameters(raw JSON Schema object 바이트)는 genai.Schema 로 직접 Unmarshal 한다.
func geminiBuildTools(tools []domainllmprovider.ToolDefinition) ([]*genai.Tool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
		}
		if len(t.Parameters) > 0 {
			var schema genai.Schema
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("gemini: %w: tool %q parameters schema: %v", domainllmprovider.ErrUpstream, t.Name, err)
			}
			decl.Parameters = &schema
		}
		decls = append(decls, decl)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}
