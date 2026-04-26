package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/domain/port"
)

const maxIterations = 5

type AgenticLoop struct {
	llm      port.LLMPort
	mcp      port.MCPClientPort
	sessions port.SessionPort
	tools    []domain.Tool
	logger   *zap.Logger
}

func NewAgenticLoop(
	llm port.LLMPort,
	mcp port.MCPClientPort,
	sessions port.SessionPort,
	logger *zap.Logger,
) *AgenticLoop {
	return &AgenticLoop{
		llm:      llm,
		mcp:      mcp,
		sessions: sessions,
		tools:    domain.DefaultTools(),
		logger:   logger,
	}
}

func (al *AgenticLoop) StartSession(ctx context.Context, clientID string) (*domain.Session, error) {
	session := domain.NewSession(uuid.New().String(), clientID)
	if err := al.sessions.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

func (al *AgenticLoop) GetSession(ctx context.Context, sessionID string) (*domain.Session, error) {
	return al.sessions.GetSession(ctx, sessionID)
}

func (al *AgenticLoop) RunAgenticLoop(ctx context.Context, sessionID, clientID, userMessage string) (string, error) {
	session, err := al.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}

	session.AddMessage(domain.Message{
		ID:        uuid.New().String(),
		Role:      domain.RoleUser,
		Content:   userMessage,
		CreatedAt: time.Now(),
	})

	messages := al.buildMessages(session)

	for i := 0; i < maxIterations; i++ {
		al.logger.Info("agentic loop", zap.Int("iteration", i+1), zap.String("sessionID", sessionID))

		response, err := al.llm.Chat(ctx, messages, al.tools)
		if err != nil {
			return "", fmt.Errorf("llm chat: %w", err)
		}

		// 도구 호출 파싱 — LLM 응답에서 JSON 추출
		response.ToolCall = parseToolCall(response.Content)

		session.AddMessage(*response)
		messages = append(messages, *response)

		// 도구 호출이 없으면 최종 답변
		if response.ToolCall == nil {
			if err := al.sessions.UpdateSession(ctx, session); err != nil {
				al.logger.Error("update session", zap.Error(err))
			}
			return response.Content, nil
		}

		al.logger.Info("tool call",
			zap.String("tool", response.ToolCall.ToolName),
			zap.String("clientID", clientID),
		)

		result, err := al.mcp.ExecuteTool(ctx, clientID, *response.ToolCall)
		if err != nil {
			al.logger.Error("tool execution failed", zap.Error(err))
			result = &domain.ToolResult{
				ToolCallID: response.ToolCall.ID,
				Error:      err.Error(),
			}
		}

		toolMsg := domain.Message{
			ID:         uuid.New().String(),
			Role:       domain.RoleTool,
			Content:    result.Output,
			ToolResult: result,
			CreatedAt:  time.Now(),
		}
		session.AddMessage(toolMsg)
		messages = append(messages, toolMsg)
	}

	if err := al.sessions.UpdateSession(ctx, session); err != nil {
		al.logger.Error("update session", zap.Error(err))
	}
	return "최대 반복 횟수에 도달했습니다. 현재까지의 결과를 확인해 주세요.", nil
}

func (al *AgenticLoop) buildMessages(session *domain.Session) []domain.Message {
	messages := []domain.Message{
		{
			ID:        uuid.New().String(),
			Role:      domain.RoleSystem,
			Content:   al.buildSystemPrompt(),
			CreatedAt: time.Now(),
		},
	}
	return append(messages, session.Messages...)
}

func (al *AgenticLoop) buildSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString(`당신은 원격 Windows 시스템을 관리하는 AI 에이전트입니다.
사용자의 요청을 분석하고, 필요한 경우 아래 도구를 사용하여 원격 시스템에서 작업을 수행합니다.

[도구 호출 형식]
도구가 필요할 때는 반드시 아래 JSON 형식으로만 응답하세요 (다른 텍스트 포함 금지):
{"tool_call":{"name":"도구이름","parameters":{"파라미터명":"값"}}}

도구가 필요 없을 때는 일반 텍스트로 응답하세요.

PowerShell 스크립트 작성 요청 시에는 execute_powershell 도구를 사용하여 스크립트를 직접 실행하세요.

[사용 가능한 도구]
`)

	for _, tool := range al.tools {
		sb.WriteString(fmt.Sprintf("\n### %s\n%s\n", tool.Name, tool.Description))
		if len(tool.Parameters) > 0 {
			sb.WriteString("파라미터:\n")
			for _, p := range tool.Parameters {
				req := ""
				if p.Required {
					req = " (필수)"
				}
				sb.WriteString(fmt.Sprintf("  - %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description))
			}
		}
	}

	sb.WriteString("\n항상 한국어로 응답하세요.")
	return sb.String()
}

// parseToolCall은 LLM 응답 텍스트에서 도구 호출 JSON을 추출합니다.
func parseToolCall(content string) *domain.ToolCall {
	start := strings.Index(content, `{"tool_call"`)
	if start == -1 {
		return nil
	}

	depth, end := 0, -1
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		return nil
	}

	var wrapper struct {
		ToolCall struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"tool_call"`
	}
	if err := json.Unmarshal([]byte(content[start:end]), &wrapper); err != nil {
		return nil
	}

	return &domain.ToolCall{
		ID:         uuid.New().String(),
		ToolName:   wrapper.ToolCall.Name,
		Parameters: wrapper.ToolCall.Parameters,
	}
}
