// Package quotaapp 는 일/주/월 토큰 쿼터 시행/관리 유스케이스를 담는다.
// 한도 = 윈도우별로 멤버 값(>0) 우선, 없으면 전역 기본값. 0 이면 그 윈도우는 무제한.
package quotaapp

import (
	"context"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

// defaultLimitsTTL 은 전역 기본한도 캐시 수명이다(핫패스 쿼리 제거 + 다중 인스턴스 전파 상한).
const defaultLimitsTTL = 30 * time.Second

// accessGate 는 admin 인가 게이트다(auth 도메인 직접 import 회피).
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// cachedLimits 는 만료 시각과 함께 캐시된 전역 기본한도다.
type cachedLimits struct {
	limits  domainquota.Limits
	expires time.Time
}

// Service 는 토큰 쿼터 시행/관리 서비스다.
type Service struct {
	repo     domainquota.Repository
	gate     accessGate
	now      func() time.Time
	defCache atomic.Pointer[cachedLimits]
}

// NewService 는 Service 를 생성한다.
func NewService(repo domainquota.Repository, gate accessGate) *Service {
	return &Service{repo: repo, gate: gate, now: time.Now}
}

// defaultLimits 는 전역 기본한도를 TTL 캐시로 반환한다(만료 시 재조회). 캐시 미스는 정확성 무해.
func (s *Service) defaultLimits(ctx context.Context) (domainquota.Limits, error) {
	if c := s.defCache.Load(); c != nil && s.now().Before(c.expires) {
		return c.limits, nil
	}
	l, err := s.repo.DefaultLimits(ctx)
	if err != nil {
		return domainquota.Limits{}, err
	}
	s.defCache.Store(&cachedLimits{limits: l, expires: s.now().Add(defaultLimitsTTL)})
	return l, nil
}

// Check 는 일/주/월 중 하나라도 한도를 이미 초과했으면 ErrExceeded 를 반환한다(스트리밍 시작 전).
func (s *Service) Check(ctx context.Context, memberID string) error {
	limits, err := s.effectiveLimits(ctx, memberID)
	if err != nil {
		return err
	}
	now := s.now()
	// 한도가 설정된(>0) 윈도우의 기간 키만 모아 단일 쿼리로 사용량을 조회한다(무제한 윈도우는 스킵).
	periods := make([]string, 0, len(domainquota.Windows))
	for _, w := range domainquota.Windows {
		if limits.Get(w) > 0 {
			periods = append(periods, domainquota.PeriodKey(w, now))
		}
	}
	if len(periods) == 0 {
		return nil // 전부 무제한
	}
	usage, err := s.repo.UsageForPeriods(ctx, memberID, periods)
	if err != nil {
		return err
	}
	// 시행 순서(일→주→월) 유지: 먼저 초과한 윈도우를 반환.
	for _, w := range domainquota.Windows {
		limit := limits.Get(w)
		if limit <= 0 {
			continue
		}
		period := domainquota.PeriodKey(w, now)
		if used := usage[period]; used >= limit {
			slog.Debug("quota exceeded", "event", "quota.check", "member_id", memberID, "window", string(w), "used", used, "limit", limit)
			return &domainquota.ExceededError{
				Window: w, Used: used, Limit: limit,
				Period: period, ResetUTC: domainquota.ResetAfter(w, now),
			}
		}
	}
	return nil
}

// Add 는 사용 토큰을 일/주/월 카운터에 누적한다(응답 후 호출, 단일 멀티로우 upsert). 실패는 로깅만(요청 결과 무영향).
func (s *Service) Add(ctx context.Context, memberID string, tokens int) {
	if tokens <= 0 || memberID == "" {
		return
	}
	now := s.now()
	periods := make([]string, len(domainquota.Windows))
	for i, w := range domainquota.Windows {
		periods[i] = domainquota.PeriodKey(w, now)
	}
	if err := s.repo.AddUsage(ctx, memberID, periods, tokens); err != nil {
		slog.Warn("quota add failed", "event", "quota.add", "member_id", memberID, "tokens", tokens, "error", err)
	}
}

// effectiveLimits 는 윈도우별로 멤버 값(>0) 우선, 없으면 전역 기본값을 합성한다.
func (s *Service) effectiveLimits(ctx context.Context, memberID string) (domainquota.Limits, error) {
	member, err := s.repo.MemberLimits(ctx, memberID)
	if err != nil {
		return domainquota.Limits{}, err
	}
	def, err := s.defaultLimits(ctx)
	if err != nil {
		return domainquota.Limits{}, err
	}
	return domainquota.Limits{
		Daily:   pick(member.Daily, def.Daily),
		Weekly:  pick(member.Weekly, def.Weekly),
		Monthly: pick(member.Monthly, def.Monthly),
	}, nil
}

func pick(member, def int) int {
	if member > 0 {
		return member
	}
	return def
}

// Status 는 호출자 본인의 일/주/월 쿼터 현황(한도·사용·잔여·사용률)을 반환한다(게이트 없음 — 본인 조회).
func (s *Service) Status(ctx context.Context, memberID string) (domainquota.Status, error) {
	limits, err := s.effectiveLimits(ctx, memberID)
	if err != nil {
		return domainquota.Status{}, err
	}
	now := s.now()
	st := domainquota.Status{Period: domainquota.PeriodKeys{
		Daily:   domainquota.PeriodKey(domainquota.Daily, now),
		Weekly:  domainquota.PeriodKey(domainquota.Weekly, now),
		Monthly: domainquota.PeriodKey(domainquota.Monthly, now),
	}}
	periods := make([]string, len(domainquota.Windows))
	for i, w := range domainquota.Windows {
		periods[i] = domainquota.PeriodKey(w, now)
	}
	usage, err := s.repo.UsageForPeriods(ctx, memberID, periods)
	if err != nil {
		return domainquota.Status{}, err
	}
	for _, w := range domainquota.Windows {
		limit := limits.Get(w)
		used := usage[domainquota.PeriodKey(w, now)]
		ws := domainquota.WindowStatus{Window: w, Limit: limit, Used: used, Unlimited: limit <= 0}
		if limit > 0 {
			if rem := limit - used; rem > 0 {
				ws.Remaining = rem
			}
			pct := float64(used) / float64(limit) * 100
			if pct > 100 {
				pct = 100
			}
			ws.PercentUsed = math.Round(pct*10) / 10 // 소수 1자리
		}
		st.Windows = append(st.Windows, ws)
	}
	return st, nil
}

// --- 어드민(읽기는 페이지가 이미 admin 게이트, 쓰기는 RequireAdmin) ---

// SnapshotFor 는 주어진 멤버들의 전역 기본값 + 한도/사용량(이번 일·주·월) + 기간 키를 반환한다.
//
// 조회 범위가 항상 인자로 들어온 멤버로 묶인다 — 어드민 목록은 한 페이지(수십~수백 명)만
// 뿌리므로, 한도·사용량 테이블을 통째로 읽으면 보유량이 표시 대상이 아니라 전체 멤버 수에 비례한다.
func (s *Service) SnapshotFor(ctx context.Context, memberIDs []string) (domainquota.Snapshot, error) {
	keys := s.periodKeys()
	def, err := s.repo.DefaultLimits(ctx)
	if err != nil {
		return domainquota.Snapshot{}, err
	}
	snap := domainquota.Snapshot{
		Default: def,
		Keys:    keys,
		Limits:  map[string]domainquota.Limits{},
		Usage:   map[string]domainquota.Limits{},
	}
	if len(memberIDs) == 0 {
		return snap, nil // 조회할 대상이 없다(빈 IN 절은 유효한 SQL 이 아니다).
	}

	if snap.Limits, err = s.repo.MemberLimitsByIDs(ctx, memberIDs); err != nil {
		return domainquota.Snapshot{}, err
	}
	usage := make(map[string]domainquota.Limits, len(memberIDs))
	for _, step := range []struct {
		key string
		set func(*domainquota.Limits, int)
	}{
		{keys.Daily, func(l *domainquota.Limits, v int) { l.Daily = v }},
		{keys.Weekly, func(l *domainquota.Limits, v int) { l.Weekly = v }},
		{keys.Monthly, func(l *domainquota.Limits, v int) { l.Monthly = v }},
	} {
		m, err := s.repo.UsageByPeriodForMembers(ctx, step.key, memberIDs)
		if err != nil {
			return domainquota.Snapshot{}, err
		}
		for id, v := range m {
			u := usage[id]
			step.set(&u, v)
			usage[id] = u
		}
	}
	snap.Usage = usage
	return snap, nil
}

// periodKeys 는 현재 시각의 일/주/월 기간 키를 만든다.
func (s *Service) periodKeys() domainquota.PeriodKeys {
	now := s.now()
	return domainquota.PeriodKeys{
		Daily:   domainquota.PeriodKey(domainquota.Daily, now),
		Weekly:  domainquota.PeriodKey(domainquota.Weekly, now),
		Monthly: domainquota.PeriodKey(domainquota.Monthly, now),
	}
}

// SetDefaultLimits 는 전역 기본 한도를 설정한다(admin↑). 음수는 0(무제한)으로 보정.
func (s *Service) SetDefaultLimits(ctx context.Context, actorID string, l domainquota.Limits) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	if err := s.repo.SetDefaultLimits(ctx, clamp(l)); err != nil {
		return err
	}
	s.defCache.Store(nil) // 즉시 재조회 유도
	return nil
}

// ResetUsage 는 멤버의 사용량을 초기화한다(admin↑).
func (s *Service) ResetUsage(ctx context.Context, actorID, memberID string) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	return s.repo.ResetUsage(ctx, memberID)
}

// SetMemberLimits 는 멤버별 한도를 설정한다(admin↑). 0 = 전역 기본값 사용.
func (s *Service) SetMemberLimits(ctx context.Context, actorID, memberID string, l domainquota.Limits) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	return s.repo.SetMemberLimits(ctx, memberID, clamp(l))
}

func clamp(l domainquota.Limits) domainquota.Limits {
	if l.Daily < 0 {
		l.Daily = 0
	}
	if l.Weekly < 0 {
		l.Weekly = 0
	}
	if l.Monthly < 0 {
		l.Monthly = 0
	}
	return l
}
