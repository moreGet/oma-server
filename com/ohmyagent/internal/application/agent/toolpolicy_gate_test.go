package agentapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainagent "aiagent/com/ohmyagent/internal/domain/agent"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// 도구 정책 게이트. 서버는 도구를 실행하지 않으므로 실행을 막을 수는 없지만,
// 차단된 도구 스키마를 LLM 에 넘기지 않음으로써 모델이 그 도구를 호출하도록
// 유도되는 경로를 끊는다.

type fakePolicy struct {
	enabled, disabled []string
	lastMemberID      string
}

func (p *fakePolicy) EffectivePolicy(memberID string) (string, []string, []string) {
	p.lastMemberID = memberID
	return "cached", p.enabled, p.disabled
}

type fakeAdapter struct{ called bool }

func (a *fakeAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	a.called = true
	return onChunk(domainllmprovider.ChatStreamChunk{Done: true})
}
func (a *fakeAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

type fakeProviders struct{ adapter *fakeAdapter }

func (p *fakeProviders) GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error) {
	return p.adapter, nil
}

func newSvc(policy *fakePolicy) (*AgentService, *fakeAdapter) {
	adapter := &fakeAdapter{}
	return NewAgentService(&fakeProviders{adapter: adapter}, policy), adapter
}

func cmdWithTools(names ...string) domainagent.ChatCommand {
	tools := make([]domainagent.ToolDefinition, 0, len(names))
	for _, n := range names {
		tools = append(tools, domainagent.ToolDefinition{Name: n})
	}
	return domainagent.ChatCommand{
		Messages: []domainagent.Message{{Role: domainagent.RoleUser, Content: "hi"}},
		Tools:    tools,
		ActorID:  "member-1",
	}
}

func drain(domainagent.Event) error { return nil }

func TestToolPolicyGate_BlockedToolRejectsRequest(t *testing.T) {
	svc, adapter := newSvc(&fakePolicy{disabled: []string{"run_command"}})

	err := svc.Stream(context.Background(), cmdWithTools("read_file", "run_command"), drain)

	var tb *domainagent.ErrToolsBlocked
	require.ErrorAs(t, err, &tb)
	require.Len(t, tb.Tools, 1)
	assert.Equal(t, "run_command", tb.Tools[0].Name)
	assert.Equal(t, domaintoolpolicy.ReasonBlocked, tb.Tools[0].Reason)

	// 업스트림 호출 전에 막아야 한다 — 차단 도구가 모델에 노출되면 안 된다.
	assert.False(t, adapter.called, "차단 시 LLM 을 호출하면 안 된다")
}

func TestToolPolicyGate_AllowedToolsPassThrough(t *testing.T) {
	svc, adapter := newSvc(&fakePolicy{disabled: []string{"run_command"}})

	require.NoError(t, svc.Stream(context.Background(), cmdWithTools("read_file"), drain))
	assert.True(t, adapter.called)
}

func TestToolPolicyGate_EmptyPolicyAllowsEverything(t *testing.T) {
	svc, adapter := newSvc(&fakePolicy{})

	require.NoError(t, svc.Stream(context.Background(), cmdWithTools("run_command", "anything"), drain))
	assert.True(t, adapter.called)
}

// enabled 화이트리스트가 있으면 목록 밖 도구는 거부되고 사유가 다르다.
func TestToolPolicyGate_AllowlistRejectsUnlisted(t *testing.T) {
	svc, _ := newSvc(&fakePolicy{enabled: []string{"read_file"}})

	err := svc.Stream(context.Background(), cmdWithTools("write_file"), drain)

	var tb *domainagent.ErrToolsBlocked
	require.ErrorAs(t, err, &tb)
	require.Len(t, tb.Tools, 1)
	assert.Equal(t, domaintoolpolicy.ReasonNotInAllowlist, tb.Tools[0].Reason)
}

// 차단 도구가 여러 개면 전부 알려줘야 클라이언트가 한 번에 고칠 수 있다.
func TestToolPolicyGate_ReportsAllBlockedTools(t *testing.T) {
	svc, _ := newSvc(&fakePolicy{disabled: []string{"run_command", "manage_todos"}})

	err := svc.Stream(context.Background(), cmdWithTools("run_command", "read_file", "manage_todos"), drain)

	var tb *domainagent.ErrToolsBlocked
	require.ErrorAs(t, err, &tb)
	assert.Len(t, tb.Tools, 2)
	assert.Contains(t, err.Error(), "run_command")
	assert.Contains(t, err.Error(), "manage_todos")
}

// 정책은 요청자(ActorID)기준으로 조회되어야 한다 — 멤버별 오버라이드가 걸리는 지점.
func TestToolPolicyGate_ResolvesPolicyForRequestingMember(t *testing.T) {
	policy := &fakePolicy{disabled: []string{"x"}}
	svc, _ := newSvc(policy)

	_ = svc.Stream(context.Background(), cmdWithTools("read_file"), drain)

	assert.Equal(t, "member-1", policy.lastMemberID)
}

// 도구를 안 보내는 일반 채팅은 정책과 무관하게 통과해야 한다.
func TestToolPolicyGate_NoToolsIsUnaffected(t *testing.T) {
	svc, adapter := newSvc(&fakePolicy{disabled: []string{"run_command"}})

	cmd := domainagent.ChatCommand{
		Messages: []domainagent.Message{{Role: domainagent.RoleUser, Content: "hi"}},
		ActorID:  "member-1",
	}
	require.NoError(t, svc.Stream(context.Background(), cmd, drain))
	assert.True(t, adapter.called)
}
