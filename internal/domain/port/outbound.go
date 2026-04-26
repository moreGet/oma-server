package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// LLMPort는 로컬 LLM(Gemma/Ollama)과의 통신 인터페이스입니다.
type LLMPort interface {
	Chat(ctx context.Context, messages []domain.Message, tools []domain.Tool) (*domain.Message, error)
}

// MCPCommand는 C# 클라이언트로 전송하는 JSON-RPC 2.0 명령입니다.
type MCPCommand struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
	ID      string         `json:"id"`
}

// MCPClientPort는 C# 클라이언트 관리 및 MCP 명령 전달 인터페이스입니다.
type MCPClientPort interface {
	RegisterClient(ctx context.Context, client *domain.Client) error
	ExecuteTool(ctx context.Context, clientID string, toolCall domain.ToolCall) (*domain.ToolResult, error)
	GetOnlineClients(ctx context.Context) ([]*domain.Client, error)
	// SSE 스트림 관리
	SubscribeClient(clientID string) (<-chan MCPCommand, error)
	UnsubscribeClient(clientID string)
	// C# 클라이언트로부터 도구 실행 결과 수신
	PublishResult(clientID string, result domain.ToolResult) error
}

// SessionPort는 세션 영속성 인터페이스입니다.
type SessionPort interface {
	CreateSession(ctx context.Context, session *domain.Session) error
	GetSession(ctx context.Context, sessionID string) (*domain.Session, error)
	UpdateSession(ctx context.Context, session *domain.Session) error
	DeleteSession(ctx context.Context, sessionID string) error
}
