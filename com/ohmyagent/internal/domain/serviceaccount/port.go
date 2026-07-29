package serviceaccount

import (
	"context"
	"time"
)

// Service 는 관리 유스케이스 in 포트다(관리 API 6개가 소비).
// 주의: API키 인증(Authenticate)은 domainauth.Claims 를 반환해야 하므로 도메인 무의존을 위해
// 이 포트에 두지 않고, 애플리케이션 유스케이스가 security.APIKeyAuthenticator 를 별도 구현한다.
type Service interface {
	CreateAccount(ctx context.Context, cmd CreateAccountCommand) (ServiceAccount, error)
	ListAccounts(ctx context.Context, actorID string) ([]AccountWithKeys, error)          // 키 메타 포함, 평문 제외
	DeleteAccount(ctx context.Context, actorID, id string) error                          // 계정+딸린 키 전부 무효
	IssueKey(ctx context.Context, cmd IssueKeyCommand) (ServiceAccountKey, string, error) // (키, 평문토큰)
	ListKeys(ctx context.Context, actorID, accountID string) ([]ServiceAccountKey, error)
	RevokeKey(ctx context.Context, actorID, accountID, keyID string) error
}

// AccountWithKeys 는 계정 + 그 계정의 키 메타 목록을 묶는다(목록 응답용).
type AccountWithKeys struct {
	Account ServiceAccount
	Keys    []ServiceAccountKey
}

// Repository 는 계정+키 영속화 out 포트다. DB 어댑터가 구현한다.
type Repository interface {
	// 계정
	SaveAccount(ctx context.Context, a ServiceAccount) error
	FindAccountByID(ctx context.Context, id string) (ServiceAccount, error) // 없으면 ErrNotFound
	ListAccounts(ctx context.Context) ([]ServiceAccount, error)             // 활성만(revoked_at=0)
	RevokeAccount(ctx context.Context, id string, now time.Time) error      // 0행 → ErrNotFound
	// 키
	SaveKey(ctx context.Context, k ServiceAccountKey) error
	ListKeysByAccount(ctx context.Context, accountID string) ([]ServiceAccountKey, error)
	// ListKeysByAccounts 는 여러 계정의 키를 한 번에 조회해 계정ID별로 묶어 반환한다.
	// 목록 화면이 계정마다 개별 조회(N+1)를 내지 않게 하는 배치 경로다.
	ListKeysByAccounts(ctx context.Context, accountIDs []string) (map[string][]ServiceAccountKey, error)
	RevokeKey(ctx context.Context, accountID, keyID string, now time.Time) error // 0행 → ErrKeyNotFound
	RevokeKeysByAccount(ctx context.Context, accountID string, now time.Time) error
	// 인증(단일 JOIN): 해시로 (키 + 소속 계정)을 한 번에 조회. 없으면 ErrKeyNotFound.
	FindKeyForAuth(ctx context.Context, tokenHash string) (ServiceAccountKey, ServiceAccount, error)
	// TouchKeyLastUsed 는 last_used_at 을 best-effort 갱신한다(스로틀은 유스케이스가 판단).
	TouchKeyLastUsed(ctx context.Context, keyID string, now time.Time) error
}

// TokenHasher 는 불투명 토큰 생성 + SHA-256 해시 out 포트다. crypto 어댑터가 구현한다.
type TokenHasher interface {
	// NewToken 은 "oma_sa_<random>" 평문과 그 SHA-256 hex 해시를 함께 반환한다.
	NewToken() (plain, hash string, err error)
	// Hash 는 raw 토큰(접두사 포함)을 SHA-256 hex 로 해시한다(인증 시 대조용).
	Hash(raw string) string
}
