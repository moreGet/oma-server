package httpin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// fakeSAService 는 serviceAccountService 페이크다(주입 가능한 반환/오류 + 호출 인자 기록).
type fakeSAService struct {
	createAcct   domainserviceaccount.ServiceAccount
	createErr    error
	lastCreate   domainserviceaccount.CreateAccountCommand
	listAccounts []domainserviceaccount.AccountWithKeys
	listErr      error
	deleteErr    error
	lastDeleteID string
	issuedKey    domainserviceaccount.ServiceAccountKey
	issuedToken  string
	issueErr     error
	lastIssue    domainserviceaccount.IssueKeyCommand
	listKeys     []domainserviceaccount.ServiceAccountKey
	listKeysErr  error
	revokeErr    error
	lastRevokeA  string
	lastRevokeK  string
}

var _ serviceAccountService = (*fakeSAService)(nil)

func (s *fakeSAService) CreateAccount(_ context.Context, cmd domainserviceaccount.CreateAccountCommand) (domainserviceaccount.ServiceAccount, error) {
	s.lastCreate = cmd
	return s.createAcct, s.createErr
}
func (s *fakeSAService) ListAccounts(_ context.Context, _ string) ([]domainserviceaccount.AccountWithKeys, error) {
	return s.listAccounts, s.listErr
}
func (s *fakeSAService) DeleteAccount(_ context.Context, _, id string) error {
	s.lastDeleteID = id
	return s.deleteErr
}
func (s *fakeSAService) IssueKey(_ context.Context, cmd domainserviceaccount.IssueKeyCommand) (domainserviceaccount.ServiceAccountKey, string, error) {
	s.lastIssue = cmd
	return s.issuedKey, s.issuedToken, s.issueErr
}
func (s *fakeSAService) ListKeys(_ context.Context, _, _ string) ([]domainserviceaccount.ServiceAccountKey, error) {
	return s.listKeys, s.listKeysErr
}
func (s *fakeSAService) RevokeKey(_ context.Context, _, accountID, keyID string) error {
	s.lastRevokeA, s.lastRevokeK = accountID, keyID
	return s.revokeErr
}

func newTestRouterWithSA(svc serviceAccountService) (*security.SecureRouter, *security.JWTTokenService) {
	tok := security.NewJWTTokenService("test-secret", time.Hour)
	r := security.NewSecureRouter(http.NewServeMux(), tok)
	h := NewServiceAccountHandler(svc)
	r.Secured("POST /api/v1/service-accounts", Handle(h.Create), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("GET /api/v1/service-accounts", Handle(h.List), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("DELETE /api/v1/service-accounts/{id}", Handle(h.Delete), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("POST /api/v1/service-accounts/{id}/keys", Handle(h.IssueKey), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("GET /api/v1/service-accounts/{id}/keys", Handle(h.ListKeys), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("DELETE /api/v1/service-accounts/{id}/keys/{key_id}", Handle(h.RevokeKey), security.MinRole(domainauth.RoleLevelAdmin))
	return r, tok
}

func adminAuth(t *testing.T, tok *security.JWTTokenService) string {
	return "Bearer " + tokenForLevel(t, tok, "admin-1", domainauth.RoleLevelAdmin)
}

func TestServiceAccountHandler_Create(t *testing.T) {
	t.Run("성공 201 + actor 는 JWT claims", func(t *testing.T) {
		svc := &fakeSAService{createAcct: domainserviceaccount.ServiceAccount{ID: "sa-1", Name: "bot"}}
		r, tok := newTestRouterWithSA(svc)

		body, _ := json.Marshal(map[string]any{"name": "bot", "owner_member_id": "m-1", "description": "d"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts", bytes.NewReader(body))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp saCreateResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "sa-1", resp.ID)
		assert.Equal(t, "bot", resp.Name)
		assert.Equal(t, "admin-1", svc.lastCreate.ActorID, "actor 는 본문이 아닌 JWT 에서")
		assert.Equal(t, "m-1", svc.lastCreate.OwnerMemberID)
	})

	t.Run("검증 실패 → 400", func(t *testing.T) {
		svc := &fakeSAService{createErr: &domainserviceaccount.ErrValidation{Msg: "owner_member_id does not exist"}}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts", bytes.NewReader([]byte(`{"name":"x"}`)))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})

	t.Run("user 레벨 토큰 → 403(admin 게이트)", func(t *testing.T) {
		r, tok := newTestRouterWithSA(&fakeSAService{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("인증 없으면 401", func(t *testing.T) {
		r, _ := newTestRouterWithSA(&fakeSAService{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts", bytes.NewReader([]byte(`{}`)))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestServiceAccountHandler_List(t *testing.T) {
	t.Run("성공 200 + 키 메타 포함, 평문 미노출", func(t *testing.T) {
		created := time.Unix(1_753_600_000, 0).UTC()
		svc := &fakeSAService{listAccounts: []domainserviceaccount.AccountWithKeys{{
			Account: domainserviceaccount.ServiceAccount{ID: "sa-1", Name: "bot", Description: "d", OwnerMemberID: "m-1", CreatedAt: created},
			Keys: []domainserviceaccount.ServiceAccountKey{
				{ID: "k-1", CreatedAt: created, ExpiresAt: time.Time{}, LastUsedAt: time.Time{}},
			},
		}}}
		r, tok := newTestRouterWithSA(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/service-accounts", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "token", "목록에 평문 token 필드 미노출")

		var resp saListResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.ServiceAccounts, 1)
		acc := resp.ServiceAccounts[0]
		assert.Equal(t, "sa-1", acc.ID)
		assert.Equal(t, "m-1", acc.OwnerMemberID)
		assert.Equal(t, created.Unix(), acc.CreatedAt)
		require.Len(t, acc.Keys, 1)
		assert.Equal(t, "k-1", acc.Keys[0].KeyID)
		assert.Equal(t, int64(0), acc.Keys[0].ExpiresAt, "무기한 0 sentinel")
		assert.Equal(t, int64(0), acc.Keys[0].LastUsedAt, "미사용 0 sentinel")
	})
}

func TestServiceAccountHandler_Delete(t *testing.T) {
	t.Run("성공 204", func(t *testing.T) {
		svc := &fakeSAService{}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/service-accounts/sa-1", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, w.Body.String())
		assert.Equal(t, "sa-1", svc.lastDeleteID)
	})

	t.Run("미존재 → 404", func(t *testing.T) {
		svc := &fakeSAService{deleteErr: domainserviceaccount.ErrNotFound}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/service-accounts/nope", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})
}

func TestServiceAccountHandler_IssueKey(t *testing.T) {
	t.Run("성공 201 + 평문 token 노출 + expires_at 매핑", func(t *testing.T) {
		exp := time.Unix(1_800_000_000, 0).UTC()
		svc := &fakeSAService{
			issuedKey:   domainserviceaccount.ServiceAccountKey{ID: "k-1", ExpiresAt: exp},
			issuedToken: "oma_sa_PLAINTEXT",
		}
		r, tok := newTestRouterWithSA(svc)

		body, _ := json.Marshal(map[string]any{"expires_at": exp.Unix()})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/sa-1/keys", bytes.NewReader(body))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp saIssueKeyResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "k-1", resp.KeyID)
		assert.Equal(t, "oma_sa_PLAINTEXT", resp.Token, "평문 token 은 발급 응답에만")
		assert.Equal(t, exp.Unix(), resp.ExpiresAt)
		assert.Equal(t, "sa-1", svc.lastIssue.AccountID)
		assert.Equal(t, exp, svc.lastIssue.ExpiresAt)
		assert.Equal(t, "admin-1", svc.lastIssue.ActorID)
	})

	t.Run("무기한(expires_at 생략) → cmd.ExpiresAt zero", func(t *testing.T) {
		svc := &fakeSAService{issuedKey: domainserviceaccount.ServiceAccountKey{ID: "k-1"}, issuedToken: "oma_sa_x"}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/sa-1/keys", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusCreated, w.Code)
		assert.True(t, svc.lastIssue.ExpiresAt.IsZero(), "0/생략 = 무기한")
		var resp saIssueKeyResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, int64(0), resp.ExpiresAt)
	})

	t.Run("폐기/미존재 계정 → 404", func(t *testing.T) {
		svc := &fakeSAService{issueErr: domainserviceaccount.ErrNotFound}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/nope/keys", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("과거 expires_at(ErrValidation) → 400", func(t *testing.T) {
		svc := &fakeSAService{issueErr: &domainserviceaccount.ErrValidation{Msg: "expires_at must be in the future"}}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts/sa-1/keys", bytes.NewReader([]byte(`{"expires_at":1}`)))
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})
}

func TestServiceAccountHandler_ListKeys(t *testing.T) {
	t.Run("성공 200 + 평문 미노출", func(t *testing.T) {
		svc := &fakeSAService{listKeys: []domainserviceaccount.ServiceAccountKey{
			{ID: "k-1", CreatedAt: time.Unix(1_753_600_000, 0).UTC(), RevokedAt: time.Unix(1_753_600_100, 0).UTC()},
		}}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/service-accounts/sa-1/keys", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), `"token"`, "키 목록에 평문 token 미노출")
		var resp saKeyListResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Keys, 1)
		assert.Equal(t, "k-1", resp.Keys[0].KeyID)
		assert.True(t, resp.Keys[0].Revoked, "폐기 키도 목록에 노출")
	})

	t.Run("미존재 계정 → 404", func(t *testing.T) {
		svc := &fakeSAService{listKeysErr: domainserviceaccount.ErrNotFound}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/service-accounts/nope/keys", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestServiceAccountHandler_RevokeKey(t *testing.T) {
	t.Run("성공 204 + 경로 파라미터 전달", func(t *testing.T) {
		svc := &fakeSAService{}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/service-accounts/sa-1/keys/k-9", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "sa-1", svc.lastRevokeA)
		assert.Equal(t, "k-9", svc.lastRevokeK)
	})

	t.Run("미존재 키 → 404", func(t *testing.T) {
		svc := &fakeSAService{revokeErr: domainserviceaccount.ErrKeyNotFound}
		r, tok := newTestRouterWithSA(svc)
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/service-accounts/sa-1/keys/nope", nil)
		req.Header.Set("Authorization", adminAuth(t, tok))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})
}
