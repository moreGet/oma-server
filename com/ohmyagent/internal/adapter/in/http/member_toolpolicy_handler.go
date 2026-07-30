package httpin

import (
	"context"
	"net/http"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// memberToolPolicyManager 는 멤버별 도구 정책 조회/갱신 유스케이스다(*toolpolicyapp.Manager 가 충족).
type memberToolPolicyManager interface {
	GetMemberPolicy(ctx context.Context, actorID, memberID string) (domaintoolpolicy.MemberPolicy, error)
	UpdateMemberPolicy(ctx context.Context, cmd domaintoolpolicy.MemberUpdateCommand) error
}

// MemberToolPolicyHandler 는 멤버별 도구 정책 관리 API 핸들러다(admin, 평면 envelope).
type MemberToolPolicyHandler struct {
	mgr memberToolPolicyManager
}

// NewMemberToolPolicyHandler 는 MemberToolPolicyHandler 를 생성한다.
func NewMemberToolPolicyHandler(mgr memberToolPolicyManager) *MemberToolPolicyHandler {
	return &MemberToolPolicyHandler{mgr: mgr}
}

type memberToolPolicyReq struct {
	Enabled  []string `json:"enabled"`
	Disabled []string `json:"disabled"`
}

type memberToolPolicyResp struct {
	MemberID  string   `json:"member_id"`
	Enabled   []string `json:"enabled"`
	Disabled  []string `json:"disabled"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
}

// Get 은 멤버의 도구 정책 오버라이드를 반환한다(GET /api/v1/members/{id}/tool-policy, admin).
func (h *MemberToolPolicyHandler) Get(w http.ResponseWriter, r *http.Request) error {
	memberID := r.PathValue("id")
	p, err := h.mgr.GetMemberPolicy(r.Context(), actorID(r), memberID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toMemberToolPolicyResp(p))
	return nil
}

// Put 은 멤버의 도구 정책 오버라이드를 갱신한다(PUT /api/v1/members/{id}/tool-policy, admin).
// 빈 enabled/disabled 를 보내면 오버라이드가 해제되어 전역 정책만 적용된다.
func (h *MemberToolPolicyHandler) Put(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req memberToolPolicyReq
	if err := decodeJSON(w, r, maxJSONBytes, &req); err != nil {
		return err
	}
	memberID := r.PathValue("id")
	if err := h.mgr.UpdateMemberPolicy(r.Context(), domaintoolpolicy.MemberUpdateCommand{
		MemberID: memberID,
		Enabled:  req.Enabled,
		Disabled: req.Disabled,
		ActorID:  actorID(r),
	}); err != nil {
		return err
	}
	p, err := h.mgr.GetMemberPolicy(r.Context(), actorID(r), memberID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toMemberToolPolicyResp(p))
	return nil
}

func toMemberToolPolicyResp(p domaintoolpolicy.MemberPolicy) memberToolPolicyResp {
	return memberToolPolicyResp{
		MemberID:  p.MemberID,
		Enabled:   p.Enabled,
		Disabled:  p.Disabled,
		UpdatedAt: p.UpdatedAt,
		UpdatedBy: p.UpdatedBy,
	}
}
