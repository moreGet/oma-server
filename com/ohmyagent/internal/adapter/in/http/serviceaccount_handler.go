package httpin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// serviceAccountService 는 서비스 계정 관리 유스케이스 포트다(*serviceaccountapp.Service 가 충족).
type serviceAccountService interface {
	CreateAccount(ctx context.Context, cmd domainserviceaccount.CreateAccountCommand) (domainserviceaccount.ServiceAccount, error)
	ListAccounts(ctx context.Context, actorID string) ([]domainserviceaccount.AccountWithKeys, error)
	DeleteAccount(ctx context.Context, actorID, id string) error
	IssueKey(ctx context.Context, cmd domainserviceaccount.IssueKeyCommand) (domainserviceaccount.ServiceAccountKey, string, error)
	ListKeys(ctx context.Context, actorID, accountID string) ([]domainserviceaccount.ServiceAccountKey, error)
	RevokeKey(ctx context.Context, actorID, accountID, keyID string) error
}

// ServiceAccountHandler 는 /api/v1/service-accounts 관리 핸들러다(admin 전용, 평면 AppError envelope).
// 시각 필드는 전부 unix seconds int64 로 직렬화한다(0 sentinel = 무기한/미사용/미폐기 표현; 설계 §6/§9).
type ServiceAccountHandler struct{ svc serviceAccountService }

// NewServiceAccountHandler 는 ServiceAccountHandler 를 생성한다.
func NewServiceAccountHandler(svc serviceAccountService) *ServiceAccountHandler {
	return &ServiceAccountHandler{svc: svc}
}

// --- DTO (snake_case; 시각은 unix seconds int64, 0 sentinel) ---

type saCreateReq struct {
	Name          string `json:"name"`
	OwnerMemberID string `json:"owner_member_id"`
	Description   string `json:"description,omitempty"`
}

type saIssueKeyReq struct {
	ExpiresAt int64 `json:"expires_at,omitempty"` // unix sec, 0/생략 = 무기한
}

type saCreateResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type saKeyMetaResp struct {
	KeyID      string `json:"key_id"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`   // 0 = 무기한
	LastUsedAt int64  `json:"last_used_at"` // 0 = 미사용
	Revoked    bool   `json:"revoked"`
}

type saAccountResp struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	OwnerMemberID string          `json:"owner_member_id"`
	CreatedAt     int64           `json:"created_at"`
	Revoked       bool            `json:"revoked"`
	Keys          []saKeyMetaResp `json:"keys"`
}

type saListResp struct {
	ServiceAccounts []saAccountResp `json:"service_accounts"`
}

type saKeyListResp struct {
	Keys []saKeyMetaResp `json:"keys"`
}

type saIssueKeyResp struct {
	KeyID     string `json:"key_id"`
	Token     string `json:"token"` // 평문 토큰 — 발급 응답 1회만 노출
	ExpiresAt int64  `json:"expires_at"`
}

// unixOrZero 는 시각을 unix seconds 로 변환한다(zero-time → 0 sentinel).
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func toSAKeyMetaResp(k domainserviceaccount.ServiceAccountKey) saKeyMetaResp {
	return saKeyMetaResp{
		KeyID:      k.ID,
		CreatedAt:  unixOrZero(k.CreatedAt),
		ExpiresAt:  unixOrZero(k.ExpiresAt),
		LastUsedAt: unixOrZero(k.LastUsedAt),
		Revoked:    k.Revoked(),
	}
}

// --- 핸들러 ---

// Create 는 POST /api/v1/service-accounts — 계정 생성(admin). 201.
func (h *ServiceAccountHandler) Create(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req saCreateReq
	if err := decodeJSON(w, r, maxJSONBytes, &req); err != nil {
		return err
	}
	acct, err := h.svc.CreateAccount(r.Context(), domainserviceaccount.CreateAccountCommand{
		ActorID:       actorOf(r),
		Name:          req.Name,
		OwnerMemberID: req.OwnerMemberID,
		Description:   req.Description,
	})
	if err != nil {
		return serviceAccountErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, saCreateResp{ID: acct.ID, Name: acct.Name})
	return nil
}

// List 는 GET /api/v1/service-accounts — 계정 목록(admin, 키 메타 포함, 평문 제외). 200.
func (h *ServiceAccountHandler) List(w http.ResponseWriter, r *http.Request) error {
	accounts, err := h.svc.ListAccounts(r.Context(), actorOf(r))
	if err != nil {
		return serviceAccountErrToHTTP(err)
	}
	out := saListResp{ServiceAccounts: make([]saAccountResp, 0, len(accounts))}
	for _, aw := range accounts {
		item := saAccountResp{
			ID:            aw.Account.ID,
			Name:          aw.Account.Name,
			Description:   aw.Account.Description,
			OwnerMemberID: aw.Account.OwnerMemberID,
			CreatedAt:     unixOrZero(aw.Account.CreatedAt),
			Revoked:       aw.Account.Revoked(),
			Keys:          make([]saKeyMetaResp, 0, len(aw.Keys)),
		}
		for _, k := range aw.Keys {
			item.Keys = append(item.Keys, toSAKeyMetaResp(k))
		}
		out.ServiceAccounts = append(out.ServiceAccounts, item)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// Delete 는 DELETE /api/v1/service-accounts/{id} — 계정 폐기(+딸린 키 전부 폐기, admin). 204.
func (h *ServiceAccountHandler) Delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.DeleteAccount(r.Context(), actorOf(r), r.PathValue("id")); err != nil {
		return serviceAccountErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// IssueKey 는 POST /api/v1/service-accounts/{id}/keys — 키 발급(admin). 201, 평문 token 여기서만.
func (h *ServiceAccountHandler) IssueKey(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req saIssueKeyReq
	if err := decodeJSON(w, r, maxJSONBytes, &req); err != nil {
		return err
	}
	var expiresAt time.Time
	if req.ExpiresAt > 0 {
		expiresAt = time.Unix(req.ExpiresAt, 0).UTC()
	}
	key, token, err := h.svc.IssueKey(r.Context(), domainserviceaccount.IssueKeyCommand{
		ActorID:   actorOf(r),
		AccountID: r.PathValue("id"),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return serviceAccountErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, saIssueKeyResp{
		KeyID:     key.ID,
		Token:     token,
		ExpiresAt: unixOrZero(key.ExpiresAt),
	})
	return nil
}

// ListKeys 는 GET /api/v1/service-accounts/{id}/keys — 키 목록(admin, 폐기 포함, 평문 제외). 200.
func (h *ServiceAccountHandler) ListKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := h.svc.ListKeys(r.Context(), actorOf(r), r.PathValue("id"))
	if err != nil {
		return serviceAccountErrToHTTP(err)
	}
	out := saKeyListResp{Keys: make([]saKeyMetaResp, 0, len(keys))}
	for _, k := range keys {
		out.Keys = append(out.Keys, toSAKeyMetaResp(k))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// RevokeKey 는 DELETE /api/v1/service-accounts/{id}/keys/{key_id} — 키 폐기(admin). 204.
func (h *ServiceAccountHandler) RevokeKey(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.RevokeKey(r.Context(), actorOf(r), r.PathValue("id"), r.PathValue("key_id")); err != nil {
		return serviceAccountErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// actorOf 는 인증 claims 에서 actor(멤버) id 를 추출한다.
func actorOf(r *http.Request) string {
	claims, _ := security.ClaimsFrom(r.Context())
	return claims.MemberID
}

// --- 에러 매핑(도메인 → AppError) ---

// serviceAccountErrToHTTP 는 서비스 계정 도메인 에러를 HTTP 로 매핑한다.
// ErrValidation→400, ErrNotFound/ErrKeyNotFound→404, auth ErrPermission→403, 그 외→500(Handle).
func serviceAccountErrToHTTP(err error) error {
	var ve *domainserviceaccount.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainserviceaccount.ErrNotFound):
		return ErrNotFound("service account not found")
	case errors.Is(err, domainserviceaccount.ErrKeyNotFound):
		return ErrNotFound("service account key not found")
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("admin role required")
	default:
		return err // → Handle() 에서 500
	}
}
