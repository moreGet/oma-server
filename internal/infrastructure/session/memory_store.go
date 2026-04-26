package session

import (
	"context"
	"fmt"
	"sync"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// MemoryStore는 인메모리 세션 저장소입니다.
// 프로덕션에서는 Redis 또는 DB 기반 구현체로 교체하세요.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*domain.Session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*domain.Session),
	}
}

func (s *MemoryStore) CreateSession(_ context.Context, session *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, sessionID string) (*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	return session, nil
}

func (s *MemoryStore) UpdateSession(_ context.Context, session *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	return nil
}

func (s *MemoryStore) DeleteSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	return nil
}
