package quotaapp

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

type fakeRepo struct {
	usage  map[string]int // memberID|period
	mlimit map[string]domainquota.Limits
	def    domainquota.Limits
}

func newFakeRepo(def domainquota.Limits) *fakeRepo {
	return &fakeRepo{usage: map[string]int{}, mlimit: map[string]domainquota.Limits{}, def: def}
}
func uk(m, p string) string { return m + "|" + p }

func (r *fakeRepo) AddUsage(_ context.Context, m, p string, t int) error {
	r.usage[uk(m, p)] += t
	return nil
}
func (r *fakeRepo) GetUsage(_ context.Context, m, p string) (int, error) {
	return r.usage[uk(m, p)], nil
}
func (r *fakeRepo) UsageForPeriods(_ context.Context, m string, periods []string) (map[string]int, error) {
	out := make(map[string]int, len(periods))
	for _, p := range periods {
		if v, ok := r.usage[uk(m, p)]; ok {
			out[p] = v
		}
	}
	return out, nil
}
func (r *fakeRepo) UsageByPeriod(_ context.Context, p string) (map[string]int, error) {
	out := map[string]int{}
	for k, v := range r.usage {
		if i := strings.LastIndex(k, "|"); i >= 0 && k[i+1:] == p {
			out[k[:i]] = v
		}
	}
	return out, nil
}
func (r *fakeRepo) ResetUsage(_ context.Context, m string) error {
	for k := range r.usage {
		if i := strings.LastIndex(k, "|"); i >= 0 && k[:i] == m {
			delete(r.usage, k)
		}
	}
	return nil
}
func (r *fakeRepo) MemberLimits(_ context.Context, m string) (domainquota.Limits, error) {
	return r.mlimit[m], nil
}
func (r *fakeRepo) SetMemberLimits(_ context.Context, m string, l domainquota.Limits) error {
	r.mlimit[m] = l
	return nil
}
func (r *fakeRepo) AllMemberLimits(context.Context) (map[string]domainquota.Limits, error) {
	return r.mlimit, nil
}
func (r *fakeRepo) DefaultLimits(context.Context) (domainquota.Limits, error) { return r.def, nil }
func (r *fakeRepo) SetDefaultLimits(_ context.Context, l domainquota.Limits) error {
	r.def = l
	return nil
}

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

func TestService_DailyLimitEnforced(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{Daily: 100})
	s := NewService(r, allowGate{})
	ctx := context.Background()

	require.NoError(t, s.Check(ctx, "m1"))
	s.Add(ctx, "m1", 100)                                          // 일/주/월 각각 100 누적
	assert.ErrorIs(t, s.Check(ctx, "m1"), domainquota.ErrExceeded) // 일 한도 100 도달
}

func TestService_WeeklyLimitEnforced(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{Weekly: 50}) // 일/월 무제한, 주만 50
	s := NewService(r, allowGate{})
	ctx := context.Background()

	s.Add(ctx, "m1", 50)
	assert.ErrorIs(t, s.Check(ctx, "m1"), domainquota.ErrExceeded) // 주 한도 50 도달
}

func TestService_MemberOverrideBeatsDefault(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{Monthly: 1000})
	r.mlimit["m1"] = domainquota.Limits{Monthly: 10} // 멤버 월 한도가 더 낮음
	s := NewService(r, allowGate{})
	ctx := context.Background()

	s.Add(ctx, "m1", 10)
	assert.ErrorIs(t, s.Check(ctx, "m1"), domainquota.ErrExceeded)
}

func TestService_Status(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{Daily: 100, Weekly: 0, Monthly: 1000})
	s := NewService(r, allowGate{})
	ctx := context.Background()

	s.Add(ctx, "m1", 25) // 일/주/월 각각 25
	st, err := s.Status(ctx, "m1")
	require.NoError(t, err)

	byW := map[domainquota.Window]domainquota.WindowStatus{}
	for _, ws := range st.Windows {
		byW[ws.Window] = ws
	}
	day := byW[domainquota.Daily]
	assert.Equal(t, 100, day.Limit)
	assert.Equal(t, 25, day.Used)
	assert.Equal(t, 75, day.Remaining)
	assert.False(t, day.Unlimited)
	assert.Equal(t, 25.0, day.PercentUsed)

	week := byW[domainquota.Weekly] // 한도 0 = 무제한
	assert.True(t, week.Unlimited)
	assert.Equal(t, 0, week.Remaining)
	assert.Equal(t, 0.0, week.PercentUsed)
}

func TestService_SnapshotAndAdminOps(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{})
	s := NewService(r, allowGate{})
	ctx := context.Background()

	require.NoError(t, s.SetDefaultLimits(ctx, "admin", domainquota.Limits{Daily: 10, Weekly: 20, Monthly: 30}))
	require.NoError(t, s.SetMemberLimits(ctx, "admin", "u1", domainquota.Limits{Monthly: 5}))
	s.Add(ctx, "u1", 3) // 일/주/월 각각 3

	snap, err := s.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, domainquota.Limits{Daily: 10, Weekly: 20, Monthly: 30}, snap.Default)
	assert.Equal(t, 5, snap.Limits["u1"].Monthly)
	assert.Equal(t, 3, snap.Usage["u1"].Monthly)
	assert.Equal(t, 3, snap.Usage["u1"].Daily)
	assert.NotEmpty(t, snap.Keys.Monthly)

	// 사용량 초기화 → Snapshot 사용량 0.
	require.NoError(t, s.ResetUsage(ctx, "admin", "u1"))
	snap2, err := s.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, snap2.Usage["u1"].Monthly)
}

func TestService_ZeroLimitsUnlimited(t *testing.T) {
	r := newFakeRepo(domainquota.Limits{}) // 전부 0 = 무제한
	s := NewService(r, allowGate{})
	ctx := context.Background()

	s.Add(ctx, "m1", 9_999_999)
	assert.NoError(t, s.Check(ctx, "m1"))
}
