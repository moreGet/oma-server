package httpin

import (
	"errors"
	"net/http"
	"time"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// AgentRegistryHandler 는 에이전트 레지스트리(등록·발견·생존성·A2A 토큰 브로커) 핸들러다.
// JSON 필드명은 §공유 계약(PROMPT-go-server.md ↔ PROMPT-client-host.md SYNC 블록)과 정확히 일치해야
// C# Host 와 실기 왕복이 된다 — 필드 변경 시 두 문서를 함께 갱신할 것.
type AgentRegistryHandler struct {
	svc domainagentregistry.Service
}

// NewAgentRegistryHandler 는 AgentRegistryHandler 를 생성한다.
func NewAgentRegistryHandler(svc domainagentregistry.Service) *AgentRegistryHandler {
	return &AgentRegistryHandler{svc: svc}
}

// --- DTO (§공유 계약 snake_case) ---

type agentRegisterReq struct {
	Name         string   `json:"name"`
	EndpointURL  string   `json:"endpoint_url"`
	Capabilities []string `json:"capabilities"`
	Tags         []string `json:"tags"`
	Model        string   `json:"model"`
	Version      string   `json:"version"`
}

type agentRegisterResp struct {
	AgentID                  string `json:"agent_id"`
	LeaseTTLSeconds          int    `json:"lease_ttl_seconds"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
}

type agentHeartbeatResp struct {
	Status          string `json:"status"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds"`
}

type agentResp struct {
	AgentID         string    `json:"agent_id"`
	Name            string    `json:"name"`
	EndpointURL     string    `json:"endpoint_url"`
	Capabilities    []string  `json:"capabilities"`
	Tags            []string  `json:"tags"`
	Model           string    `json:"model"`
	Status          string    `json:"status"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"` // RFC3339(UTC)
}

type agentListResp struct {
	Agents []agentResp `json:"agents"`
}

type a2aTokenResp struct {
	Token            string `json:"token"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	AudienceAgentID  string `json:"audience_agent_id"`
}

type a2aPublicKeyResp struct {
	KID          string `json:"kid"`
	Alg          string `json:"alg"`
	PublicKeyPEM string `json:"public_key_pem"`
}

func toAgentResp(a domainagentregistry.Agent) agentResp {
	out := agentResp{
		AgentID:         a.ID,
		Name:            a.Name,
		EndpointURL:     a.EndpointURL,
		Capabilities:    a.Capabilities,
		Tags:            a.Tags,
		Model:           a.Model,
		Status:          string(a.Status),
		LastHeartbeatAt: a.LastHeartbeatAt,
	}
	// 계약: capabilities/tags 는 null 이 아닌 빈 배열로 직렬화한다.
	if out.Capabilities == nil {
		out.Capabilities = []string{}
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	return out
}

// --- 핸들러 ---

// Register 는 POST /api/v1/agents/register — (owner, name) 업서트 등록.
// 재등록도 같은 응답 형태이므로 상태코드는 일관되게 200 을 쓴다(업서트 시맨틱).
func (h *AgentRegistryHandler) Register(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[agentRegisterReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	a, err := h.svc.Register(r.Context(), domainagentregistry.RegisterCommand{
		OwnerID:      actorID(r),
		Name:         req.Name,
		EndpointURL:  req.EndpointURL,
		Capabilities: req.Capabilities,
		Tags:         req.Tags,
		Model:        req.Model,
		Version:      req.Version,
	})
	if err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, agentRegisterResp{
		AgentID:                  a.ID,
		LeaseTTLSeconds:          int(h.svc.LeaseTTL().Seconds()),
		HeartbeatIntervalSeconds: int(h.svc.HeartbeatInterval().Seconds()),
	})
	return nil
}

// Heartbeat 은 POST /api/v1/agents/{id}/heartbeat — 생존 신호(소유자만).
func (h *AgentRegistryHandler) Heartbeat(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Heartbeat(r.Context(), r.PathValue("id"), actorID(r)); err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, agentHeartbeatResp{
		Status:          string(domainagentregistry.StatusOnline),
		LeaseTTLSeconds: int(h.svc.LeaseTTL().Seconds()),
	})
	return nil
}

// Deregister 는 DELETE /api/v1/agents/{id} — 우아한 해제(소유자만). 204.
func (h *AgentRegistryHandler) Deregister(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Deregister(r.Context(), r.PathValue("id"), actorID(r)); err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// List 는 GET /api/v1/agents — 발견(기본 online+stale).
func (h *AgentRegistryHandler) List(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	agents, err := h.svc.Discover(r.Context(), domainagentregistry.Filter{
		Capability: q.Get("capability"),
		Tag:        q.Get("tag"),
		Status:     q.Get("status"),
		Query:      q.Get("q"),
		ExcludeID:  q.Get("exclude_self"),
	})
	if err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, agentListResp{Agents: mapSlice(agents, toAgentResp)})
	return nil
}

// Get 은 GET /api/v1/agents/{id} — 단건 조회.
func (h *AgentRegistryHandler) Get(w http.ResponseWriter, r *http.Request) error {
	a, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toAgentResp(a))
	return nil
}

// MintToken 은 POST /api/v1/agents/{id}/token — A2A 호출 토큰 발급(브로커). {id}=호출 대상.
// 요청 본문 없음. 대상 미존재 404. (대상별 호출 ACL 은 v2 — 지금은 인증 멤버 누구나 발급 가능.)
func (h *AgentRegistryHandler) MintToken(w http.ResponseWriter, r *http.Request) error {
	tok, err := h.svc.MintToken(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return agentRegistryErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, a2aTokenResp{
		Token:            tok.Token,
		ExpiresInSeconds: int(tok.ExpiresIn.Seconds()),
		AudienceAgentID:  tok.AudienceAgentID,
	})
	return nil
}

// PublicKey 는 GET /api/v1/agents/a2a-public-key — 수신측 서명 검증용 공개키.
// (Go 1.22+ ServeMux 는 리터럴 세그먼트가 {id} 와일드카드보다 우선이라 GET /agents/{id} 와 충돌하지 않는다.)
func (h *AgentRegistryHandler) PublicKey(w http.ResponseWriter, r *http.Request) error {
	kid, alg, pemStr := h.svc.PublicKey()
	if pemStr == "" {
		// 브로커 미구성(EnableBroker 실패/미호출) — 배선 문제이므로 500 로 드러낸다.
		return errors.New("a2a broker is not configured")
	}
	writeJSON(w, http.StatusOK, a2aPublicKeyResp{KID: kid, Alg: alg, PublicKeyPEM: pemStr})
	return nil
}

// --- 에러 매핑(도메인 → AppError) ---

// agentRegistryErrToHTTP 는 레지스트리 도메인 에러를 HTTP 로 매핑한다.
// ErrForbidden(소유자 불일치)도 §공유 계약대로 404 로 응답해 존재 여부를 은닉한다 —
// 클라이언트는 404 를 "재-register 로 자가 치유" 신호로 삼는다.
func agentRegistryErrToHTTP(err error) error {
	var ve *domainagentregistry.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainagentregistry.ErrNotFound),
		errors.Is(err, domainagentregistry.ErrForbidden):
		return ErrNotFound("agent not found")
	default:
		return err // → Handle() 에서 500
	}
}
