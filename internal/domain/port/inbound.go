package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// AgentUseCase는 에이전틱 루프의 인바운드 포트입니다.
type AgentUseCase interface {
	RunAgenticLoop(ctx context.Context, sessionID, clientID, userMessage string) (string, error)
	StartSession(ctx context.Context, clientID string) (*domain.Session, error)
	GetSession(ctx context.Context, sessionID string) (*domain.Session, error)
}
