// Package serviceaccountapp 는 서비스 계정(비대화형 계정)과 장수 API 키의 유스케이스를 담는다.
// 관리 API 6개(생성·목록·삭제·키 발급·키 목록·키 폐기)와 API키 인증(security.APIKeyAuthenticator)을
// 함께 구현한다. 인가는 authUC 를 accessGate 로 주입받아 재사용한다(의존성 역전; 설계 §4.4).
package serviceaccountapp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// 컴파일 타임 인터페이스 만족 검증.
// Service 는 관리 유스케이스 in 포트(domainserviceaccount.Service)와
// API키 인증기(security.APIKeyAuthenticator)를 함께 충족한다.
var (
	_ domainserviceaccount.Service = (*Service)(nil)
	_ security.APIKeyAuthenticator = (*Service)(nil)
)

// defaultLastUsedThrottle 은 last_used_at best-effort 갱신 최소 간격이다.
// 최근 갱신분은 스킵해 인증마다 발생하는 DB 왕복을 억제한다(타이핑-인디케이터 최적화와 동일 사상).
const defaultLastUsedThrottle = 60 * time.Second

// accessGate 는 admin 인가 + owner 실재 검증 게이트다(authUC 가 충족 — 의존성 역전).
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
	RequireActiveMember(ctx context.Context, id string) (domainauth.Member, error)
}

// Service 는 서비스 계정 유스케이스다.
type Service struct {
	repo             domainserviceaccount.Repository
	hasher           domainserviceaccount.TokenHasher
	gate             accessGate
	now              func() time.Time
	lastUsedThrottle time.Duration
}

// NewService 는 서비스 계정 유스케이스를 생성한다.
func NewService(repo domainserviceaccount.Repository, hasher domainserviceaccount.TokenHasher, gate accessGate) *Service {
	return &Service{
		repo:             repo,
		hasher:           hasher,
		gate:             gate,
		now:              time.Now,
		lastUsedThrottle: defaultLastUsedThrottle,
	}
}

// --- 관리 유스케이스(전부 admin 게이트) ---

// CreateAccount 는 서비스 계정을 생성한다(admin). owner_member_id 는 실재 member 여야 한다.
func (s *Service) CreateAccount(ctx context.Context, cmd domainserviceaccount.CreateAccountCommand) (domainserviceaccount.ServiceAccount, error) {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainserviceaccount.ServiceAccount{}, err
	}
	if err := cmd.Validate(); err != nil { // Normalize 내부 호출
		return domainserviceaccount.ServiceAccount{}, err
	}
	// owner 실재·활성 검증: owner 상태 문제(미존재/비활성)는 actor 권한과 무관하므로
	// owner 관련 validation(400)으로 매핑한다. RequireActiveMember 는 미존재→ErrNotFound,
	// 비활성→ErrPermission 을 돌려주는데, 둘 다 actor(admin) 문제가 아니라 owner 문제다.
	// 그 외(DB 오류 등)만 그대로 전파한다.
	if _, err := s.gate.RequireActiveMember(ctx, cmd.OwnerMemberID); err != nil {
		switch {
		case errors.Is(err, domainauth.ErrNotFound):
			return domainserviceaccount.ServiceAccount{}, &domainserviceaccount.ErrValidation{Msg: "owner_member_id does not exist"}
		case errors.Is(err, domainauth.ErrPermission):
			return domainserviceaccount.ServiceAccount{}, &domainserviceaccount.ErrValidation{Msg: "owner_member_id is not an active member"}
		default:
			return domainserviceaccount.ServiceAccount{}, err
		}
	}
	now := s.now().UTC().Truncate(time.Second)
	acct := domainserviceaccount.ServiceAccount{
		ID:            uuid.NewString(),
		Name:          cmd.Name,
		Description:   cmd.Description,
		OwnerMemberID: cmd.OwnerMemberID,
		CreatedAt:     now,
		UpdatedAt:     now,
		CreatedBy:     cmd.ActorID,
	}
	if err := s.repo.SaveAccount(ctx, acct); err != nil {
		return domainserviceaccount.ServiceAccount{}, err
	}
	return acct, nil
}

// ListAccounts 는 활성 계정 목록을 키 메타와 함께 반환한다(admin, 평문 키 제외).
func (s *Service) ListAccounts(ctx context.Context, actorID string) ([]domainserviceaccount.AccountWithKeys, error) {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	accounts, err := s.repo.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	// 계정마다 키를 따로 조회하면 계정 N개에 쿼리 N+1 건이 나간다. 한 번에 받아 묶는다.
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	keysByAccount, err := s.repo.ListKeysByAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domainserviceaccount.AccountWithKeys, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, domainserviceaccount.AccountWithKeys{Account: a, Keys: keysByAccount[a.ID]})
	}
	return out, nil
}

// DeleteAccount 는 계정을 폐기하고 딸린 키를 전부 폐기한다(admin, soft delete).
func (s *Service) DeleteAccount(ctx context.Context, actorID, id string) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Second)
	if err := s.repo.RevokeAccount(ctx, id, now); err != nil { // 0행 → ErrNotFound
		return err
	}
	// 계정 폐기 성공 후 딸린 키 일괄 폐기(0행이어도 에러 아님).
	return s.repo.RevokeKeysByAccount(ctx, id, now)
}

// IssueKey 는 계정에 장수 API 키를 발급한다(admin). 평문 토큰은 반환값으로만 노출한다(로그 금지).
func (s *Service) IssueKey(ctx context.Context, cmd domainserviceaccount.IssueKeyCommand) (domainserviceaccount.ServiceAccountKey, string, error) {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainserviceaccount.ServiceAccountKey{}, "", err
	}
	now := s.now().UTC().Truncate(time.Second)
	if err := cmd.Validate(now); err != nil {
		return domainserviceaccount.ServiceAccountKey{}, "", err
	}
	// 대상 계정이 실재하고 활성인지 확인(폐기 계정에 키 발급 금지).
	acct, err := s.repo.FindAccountByID(ctx, cmd.AccountID)
	if err != nil { // 없으면 ErrNotFound
		return domainserviceaccount.ServiceAccountKey{}, "", err
	}
	if acct.Revoked() {
		return domainserviceaccount.ServiceAccountKey{}, "", domainserviceaccount.ErrNotFound
	}
	plain, hash, err := s.hasher.NewToken()
	if err != nil {
		return domainserviceaccount.ServiceAccountKey{}, "", err
	}
	key := domainserviceaccount.ServiceAccountKey{
		ID:        uuid.NewString(),
		AccountID: acct.ID,
		TokenHash: hash,
		CreatedAt: now,
		ExpiresAt: cmd.ExpiresAt, // zero = 무기한
		CreatedBy: cmd.ActorID,
	}
	if err := s.repo.SaveKey(ctx, key); err != nil {
		return domainserviceaccount.ServiceAccountKey{}, "", err
	}
	return key, plain, nil
}

// ListKeys 는 계정의 키 메타 목록을 반환한다(admin, 폐기 포함, 평문 제외).
func (s *Service) ListKeys(ctx context.Context, actorID, accountID string) ([]domainserviceaccount.ServiceAccountKey, error) {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	// 계정 실재 확인(없으면 404).
	if _, err := s.repo.FindAccountByID(ctx, accountID); err != nil {
		return nil, err
	}
	return s.repo.ListKeysByAccount(ctx, accountID)
}

// RevokeKey 는 계정의 특정 키를 폐기한다(admin, soft delete).
func (s *Service) RevokeKey(ctx context.Context, actorID, accountID, keyID string) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Second)
	return s.repo.RevokeKey(ctx, accountID, keyID, now) // 0행 → ErrKeyNotFound
}

// --- API키 인증(security.APIKeyAuthenticator 구현) ---

// Authenticate 는 raw Bearer 토큰(oma_sa_ 접두사 포함)을 검증해 합성 Claims 를 반환한다(설계 §4.4).
// 실패(미존재·폐기·만료·계정폐기·DB오류)는 값 미기록 slog.Warn 후 domainauth.ErrInvalidToken → 401 로 수렴한다.
// 절대 5xx 를 반환하지 않는다(스펙 §2C: 클라이언트 무한재시도 방지).
func (s *Service) Authenticate(ctx context.Context, raw string) (domainauth.Claims, error) {
	hash := s.hasher.Hash(raw)
	key, acct, err := s.repo.FindKeyForAuth(ctx, hash) // 단일 JOIN(미존재 → ErrKeyNotFound, DB오류 → err)
	if err != nil {
		// 미존재·DB오류 모두 401 로 수렴(존재 은닉 + 5xx 금지). 토큰/해시 값은 로그에 남기지 않는다.
		slog.Warn("service account auth rejected", "reason", "key lookup failed")
		return domainauth.Claims{}, domainauth.ErrInvalidToken
	}
	now := s.now().UTC()
	if acct.Revoked() || !key.IsUsable(now) { // 폐기·만료·계정폐기
		slog.Warn("service account auth rejected", "reason", "key or account not usable", "sa_id", acct.ID)
		return domainauth.Claims{}, domainauth.ErrInvalidToken
	}
	// last_used_at best-effort 갱신: 최근 갱신분은 스킵(스로틀), 요청 취소와 무관하게 background 로 수행.
	if key.LastUsedAt.IsZero() || now.Sub(key.LastUsedAt) >= s.lastUsedThrottle {
		keyID := key.ID
		go func() {
			if err := s.repo.TouchKeyLastUsed(context.Background(), keyID, now); err != nil {
				slog.Warn("service account touch last_used failed", "error", err)
			}
		}()
	}
	return domainauth.Claims{
		MemberID: acct.ID,                  // sa.ID 를 member_id 자리에(정책 평면 동일: quota/toolpolicy 자연 조회)
		Username: acct.Name,                // 감사 로그 표시
		Level:    domainauth.RoleLevelUser, // 확정 결정 2: user 레벨 고정(최소권한)
	}, nil
}
