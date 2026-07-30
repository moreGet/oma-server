package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainquota.Repository = (*QuotaRepository)(nil)

// QuotaRepository 는 토큰 사용량/한도를 영속화한다(domainquota.Repository 구현).
// 사용량 누적은 driver 별 atomic upsert 로 다중 인스턴스 동시 갱신에도 정확하다.
type QuotaRepository struct {
	db     *sql.DB
	driver string // mysql | sqlite (upsert SQL 분기)
}

// NewQuotaRepository 는 QuotaRepository 를 생성한다.
func NewQuotaRepository(conn *sql.DB, driver string) *QuotaRepository {
	return &QuotaRepository{db: conn, driver: driver}
}

// AddUsage 는 member 의 여러 period 사용량을 단일 멀티로우 upsert 로 원자적으로 += tokens 한다.
// driver 별 atomic upsert 라 다중 인스턴스 동시 누적에도 정확하다.
func (r *QuotaRepository) AddUsage(ctx context.Context, memberID string, periods []string, tokens int) error {
	if len(periods) == 0 || tokens <= 0 {
		return nil
	}
	vals := make([]string, len(periods))
	args := make([]any, 0, len(periods)*3)
	for i, p := range periods {
		vals[i] = "(?,?,?)"
		args = append(args, memberID, p, tokens)
	}
	tail := " ON CONFLICT(member_id, period) DO UPDATE SET used_tokens = used_tokens + excluded.used_tokens" // sqlite
	if r.driver == "mysql" {
		tail = " ON DUPLICATE KEY UPDATE used_tokens = used_tokens + VALUES(used_tokens)"
	}
	q := "INSERT INTO token_usage (member_id, period, used_tokens) VALUES " + strings.Join(vals, ",") + tail
	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("quota: add usage: %w", err)
	}
	return nil
}

// UsageForPeriods 는 member 의 여러 period 사용량을 IN 절 단일 쿼리로 조회한다(없는 period 는 맵에서 생략).
// period 키 포맷이 윈도우별로 달라(일=YYYY-MM-DD / 주=YYYY-Www / 월=YYYY-MM) 충돌하지 않는다.
func (r *QuotaRepository) UsageForPeriods(ctx context.Context, memberID string, periods []string) (map[string]int, error) {
	out := make(map[string]int, len(periods))
	if len(periods) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(periods, memberID)
	err := queryEach(ctx, r.db, "quota: usage for periods", func(sc rowScanner) error {
		var p string
		var v int
		if err := sc.Scan(&p, &v); err != nil {
			return err
		}
		out[p] = v
		return nil
	}, "SELECT period, used_tokens FROM token_usage WHERE member_id=? AND period IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UsageByPeriodForMembers 는 해당 기간에서 주어진 멤버들의 사용량만 반환한다.
func (r *QuotaRepository) UsageByPeriodForMembers(ctx context.Context, period string, memberIDs []string) (map[string]int, error) {
	if len(memberIDs) == 0 {
		return map[string]int{}, nil
	}
	ph, args := inPlaceholders(memberIDs, period)
	return r.scanMap(ctx, "SELECT member_id, used_tokens FROM token_usage WHERE period=? AND member_id IN ("+ph+")", args...)
}

// ResetUsage 는 멤버의 모든 기간 사용량 행을 삭제한다(0으로 초기화).
func (r *QuotaRepository) ResetUsage(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM token_usage WHERE member_id=?", memberID); err != nil {
		return fmt.Errorf("quota: reset usage: %w", err)
	}
	return nil
}

// MemberLimits 는 멤버별 일/주/월 한도를 반환한다(없으면 0).
func (r *QuotaRepository) MemberLimits(ctx context.Context, memberID string) (domainquota.Limits, error) {
	var l domainquota.Limits
	err := r.db.QueryRowContext(ctx, "SELECT daily_limit, weekly_limit, monthly_limit FROM member_token_limits WHERE member_id=?", memberID).Scan(&l.Daily, &l.Weekly, &l.Monthly)
	if errors.Is(err, sql.ErrNoRows) {
		return domainquota.Limits{}, nil
	}
	if err != nil {
		return domainquota.Limits{}, fmt.Errorf("quota: member limits: %w", err)
	}
	return l, nil
}

// SetMemberLimits 는 멤버별 한도를 upsert 한다(UPDATE→INSERT; 어드민 단발 호출이라 레이스 무관).
func (r *QuotaRepository) SetMemberLimits(ctx context.Context, memberID string, l domainquota.Limits) error {
	res, err := r.db.ExecContext(ctx, "UPDATE member_token_limits SET daily_limit=?, weekly_limit=?, monthly_limit=? WHERE member_id=?", l.Daily, l.Weekly, l.Monthly, memberID)
	if err != nil {
		return fmt.Errorf("quota: set member limits: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO member_token_limits (member_id, daily_limit, weekly_limit, monthly_limit) VALUES (?,?,?,?)", memberID, l.Daily, l.Weekly, l.Monthly); err != nil {
		return fmt.Errorf("quota: insert member limits: %w", err)
	}
	return nil
}

// MemberLimitsByIDs 는 주어진 멤버들 중 한도가 설정된(하나라도 0 초과) 행만 반환한다.
func (r *QuotaRepository) MemberLimitsByIDs(ctx context.Context, memberIDs []string) (map[string]domainquota.Limits, error) {
	out := make(map[string]domainquota.Limits, len(memberIDs))
	if len(memberIDs) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(memberIDs)
	err := queryEach(ctx, r.db, "quota: member limits by ids", func(sc rowScanner) error {
		var id string
		var l domainquota.Limits
		if err := sc.Scan(&id, &l.Daily, &l.Weekly, &l.Monthly); err != nil {
			return err
		}
		out[id] = l
		return nil
	}, "SELECT member_id, daily_limit, weekly_limit, monthly_limit FROM member_token_limits"+
		" WHERE (daily_limit > 0 OR weekly_limit > 0 OR monthly_limit > 0) AND member_id IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DefaultLimits 는 전역 기본 한도를 반환한다.
func (r *QuotaRepository) DefaultLimits(ctx context.Context) (domainquota.Limits, error) {
	var l domainquota.Limits
	err := r.db.QueryRowContext(ctx, "SELECT default_daily_limit, default_weekly_limit, default_monthly_limit FROM quota_config WHERE id=1").Scan(&l.Daily, &l.Weekly, &l.Monthly)
	if errors.Is(err, sql.ErrNoRows) {
		return domainquota.Limits{}, nil
	}
	if err != nil {
		return domainquota.Limits{}, fmt.Errorf("quota: default limits: %w", err)
	}
	return l, nil
}

// SetDefaultLimits 는 전역 기본 한도를 upsert 한다.
func (r *QuotaRepository) SetDefaultLimits(ctx context.Context, l domainquota.Limits) error {
	res, err := r.db.ExecContext(ctx, "UPDATE quota_config SET default_daily_limit=?, default_weekly_limit=?, default_monthly_limit=? WHERE id=1", l.Daily, l.Weekly, l.Monthly)
	if err != nil {
		return fmt.Errorf("quota: set defaults: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO quota_config (id, default_daily_limit, default_weekly_limit, default_monthly_limit) VALUES (1,?,?,?)", l.Daily, l.Weekly, l.Monthly); err != nil {
		return fmt.Errorf("quota: insert defaults: %w", err)
	}
	return nil
}

// scanMap 은 (member_id, int) 2컬럼 결과를 맵으로 스캔한다.
func (r *QuotaRepository) scanMap(ctx context.Context, query string, args ...any) (map[string]int, error) {
	out := make(map[string]int)
	err := queryEach(ctx, r.db, "quota", func(sc rowScanner) error {
		var id string
		var v int
		if err := sc.Scan(&id, &v); err != nil {
			return err
		}
		out[id] = v
		return nil
	}, query, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}
