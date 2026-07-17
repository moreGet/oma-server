// Package agentapp 는 에이전트 루프 유스케이스를 담는다.
// 활성 Provider 어댑터로 메시지+도구를 중계하고, 첨부 인라인·이벤트 매핑을 수행한다.
package agentapp

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	domainagent "aiagent/com/ohmyagent/internal/domain/agent"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainagent.Service = (*AgentService)(nil)

// providerResolver 는 활성 어댑터만 얻기 위한 소비자 측 최소 인터페이스다(스펙 §4.4).
type providerResolver interface {
	GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error)
}

// AgentService 는 domainagent.Service 구현이다.
type AgentService struct {
	providers providerResolver
}

// NewAgentService 는 활성 어댑터 resolver 를 주입받아 AgentService 를 생성한다.
func NewAgentService(providers providerResolver) *AgentService {
	return &AgentService{providers: providers}
}

// Stream 은 입력을 검증하고 활성 어댑터로 스트리밍 질의를 위임하며,
// 어댑터 조각을 에이전트 이벤트(content_delta/tool_call/message_stop)로 변환한다.
func (s *AgentService) Stream(ctx context.Context, cmd domainagent.ChatCommand, onEvent func(domainagent.Event) error) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	adapter, err := s.providers.GetActiveAdapter(ctx)
	if err != nil {
		return err
	}
	req := toAdapterRequest(cmd)
	return adapter.ChatStream(ctx, req, func(chunk domainllmprovider.ChatStreamChunk) error {
		if !chunk.Done {
			if chunk.Delta != "" {
				return onEvent(domainagent.Event{Kind: domainagent.EventContentDelta, Delta: chunk.Delta})
			}
			if chunk.ThinkingDelta != "" {
				return onEvent(domainagent.Event{Kind: domainagent.EventThinkingDelta, Delta: chunk.ThinkingDelta})
			}
			return nil
		}
		// 종료 조각: 도구 호출들 → message_stop 순으로 방출.
		for i := range chunk.ToolCalls {
			tc := chunk.ToolCalls[i]
			evtc := domainagent.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			if err := onEvent(domainagent.Event{Kind: domainagent.EventToolCall, ToolCall: &evtc}); err != nil {
				return err
			}
		}
		var usage *domainagent.Usage
		if chunk.Usage != nil {
			usage = &domainagent.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		stop := domainagent.NormalizeStopReason(chunk.FinishReason, len(chunk.ToolCalls) > 0)
		return onEvent(domainagent.Event{Kind: domainagent.EventMessageStop, StopReason: stop, Usage: usage})
	})
}

// toAdapterRequest 는 에이전트 커맨드를 llmprovider 어댑터 요청으로 매핑한다(첨부 인라인 포함).
func toAdapterRequest(cmd domainagent.ChatCommand) domainllmprovider.ChatRequest {
	msgs := make([]domainllmprovider.ChatMessage, 0, len(cmd.Messages))
	for _, m := range cmd.Messages {
		lm := domainllmprovider.ChatMessage{
			Role:              domainllmprovider.ChatRole(m.Role),
			Content:           contentWithAttachments(m),
			ToolCallID:        m.ToolCallID,
			Name:              m.Name,
			Thinking:          m.Thinking,
			ThinkingSignature: m.ThinkingSignature,
		}
		for _, tc := range m.ToolCalls {
			lm.ToolCalls = append(lm.ToolCalls, domainllmprovider.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		msgs = append(msgs, lm)
	}
	var tools []domainllmprovider.ToolDefinition
	if len(cmd.Tools) > 0 {
		tools = make([]domainllmprovider.ToolDefinition, 0, len(cmd.Tools))
		for _, t := range cmd.Tools {
			tools = append(tools, domainllmprovider.ToolDefinition{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		}
	}
	var thinking *domainllmprovider.ThinkingConfig
	if cmd.Thinking != nil {
		thinking = &domainllmprovider.ThinkingConfig{
			Type:         cmd.Thinking.Type,
			BudgetTokens: cmd.Thinking.BudgetTokens,
		}
	}
	return domainllmprovider.ChatRequest{
		Messages:    msgs,
		Tools:       tools,
		Model:       cmd.Model,
		MaxTokens:   cmd.MaxTokens,
		Temperature: cmd.Temperature,
		Thinking:    thinking,
	}
}

// contentWithAttachments 는 메시지 본문에 첨부를 통합한다.
// 텍스트 계열은 디코드해 인라인하고, 그 외(이미지/PDF/바이너리)는 메타데이터 노트만 덧붙인다.
func contentWithAttachments(m domainagent.Message) string {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	var b strings.Builder
	b.WriteString(m.Content)
	for _, a := range m.Attachments {
		if domainagent.IsTextLikeAttachment(a.ContentType) {
			if data, err := base64.StdEncoding.DecodeString(a.DataBase64); err == nil {
				fmt.Fprintf(&b, "\n\n[attached file: %s (%s)]\n```\n%s\n```", a.FileName, a.ContentType, string(data))
				continue
			}
		}
		fmt.Fprintf(&b, "\n\n[attached file: %s (%s, %d bytes) — binary content not inlined]", a.FileName, a.ContentType, a.SizeBytes)
	}
	return b.String()
}
