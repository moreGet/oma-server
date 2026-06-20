// Package chatsessionapp 는 채팅 세션 동기화 유스케이스를 담는다.
package chatsessionapp

import (
	"context"
	"errors"
	"time"

	domainchatsession "aiagent/com/ohmyagent/internal/domain/chatsession"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainchatsession.Service = (*SessionService)(nil)

// SessionService 는 domainchatsession.Service 구현이다.
type SessionService struct {
	repo domainchatsession.Repository
	now  func() time.Time
}

// NewSessionService 는 SessionService 를 생성한다.
func NewSessionService(repo domainchatsession.Repository) *SessionService {
	return &SessionService{repo: repo, now: func() time.Time { return time.Now().UTC().Truncate(time.Second) }}
}

// List 는 actor 소유 세션 요약 목록을 반환한다.
func (s *SessionService) List(ctx context.Context, actorID string) ([]domainchatsession.Summary, error) {
	return s.repo.ListByOwner(ctx, actorID)
}

// Get 은 actor 소유 단일 세션을 반환한다(타인 소유/부재 → ErrNotFound).
func (s *SessionService) Get(ctx context.Context, actorID, id string) (domainchatsession.Session, error) {
	return s.repo.Get(ctx, actorID, id)
}

// Upsert 는 세션을 생성/갱신한다. 이미 타인이 소유한 ID 면 ErrPermission.
func (s *SessionService) Upsert(ctx context.Context, cmd domainchatsession.UpsertCommand) (domainchatsession.Session, error) {
	if err := cmd.Validate(); err != nil {
		return domainchatsession.Session{}, err
	}
	now := s.now()

	owner, err := s.repo.FindOwner(ctx, cmd.ID)
	switch {
	case err == nil:
		if owner != cmd.OwnerID {
			return domainchatsession.Session{}, domainchatsession.ErrPermission
		}
		existing, gerr := s.repo.Get(ctx, cmd.OwnerID, cmd.ID)
		if gerr != nil {
			return domainchatsession.Session{}, gerr
		}
		updated := domainchatsession.Session{
			ID: cmd.ID, OwnerID: cmd.OwnerID, Title: cmd.Title, Data: cmd.Data,
			CreatedAt: existing.CreatedAt, UpdatedAt: now,
		}
		if uerr := s.repo.Update(ctx, updated); uerr != nil {
			return domainchatsession.Session{}, uerr
		}
		return updated, nil
	case errors.Is(err, domainchatsession.ErrNotFound):
		created := domainchatsession.Session{
			ID: cmd.ID, OwnerID: cmd.OwnerID, Title: cmd.Title, Data: cmd.Data,
			CreatedAt: now, UpdatedAt: now,
		}
		if ierr := s.repo.Insert(ctx, created); ierr != nil {
			return domainchatsession.Session{}, ierr
		}
		return created, nil
	default:
		return domainchatsession.Session{}, err
	}
}

// Delete 는 actor 소유 세션을 삭제한다(부재 → ErrNotFound).
func (s *SessionService) Delete(ctx context.Context, actorID, id string) error {
	return s.repo.Delete(ctx, actorID, id)
}
