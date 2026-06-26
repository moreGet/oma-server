// Package quota 는 사용자별 토큰 사용 한도(쿼터) 어그리거트의 도메인 모델·포트를 담는다.
// 일/주/월 3개 기간 한도를 동시에 시행한다. 카운터·한도는 모두 DB 에 두어 다중 인스턴스(LB)에서 정확.
package quota

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrExceeded 는 토큰 한도 초과를 나타낸다. → 429
var ErrExceeded = errors.New("token quota exceeded")

// Window 는 한도 적용 기간 단위다.
type Window string

const (
	Daily   Window = "day"
	Weekly  Window = "week"
	Monthly Window = "month"
)

// Windows 는 시행/표시 순서(일→주→월)다.
var Windows = []Window{Daily, Weekly, Monthly}

// PeriodKey 는 윈도우별 기간 키를 반환한다(UTC). 세 윈도우의 키 포맷이 달라 같은 테이블에서 충돌하지 않는다.
//
//	Daily   → "2006-01-02"
//	Weekly  → "2006-W02"(ISO 주차)
//	Monthly → "2006-01"
func PeriodKey(w Window, t time.Time) string {
	tu := t.UTC()
	switch w {
	case Daily:
		return tu.Format("2006-01-02")
	case Weekly:
		y, wk := tu.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, wk)
	default:
		return tu.Format("2006-01")
	}
}

// Limits 는 일/주/월 한도(또는 사용량) 3요소 값이다. 0 = 무제한(한도) / 미사용(사용량).
type Limits struct {
	Daily   int
	Weekly  int
	Monthly int
}

// Get 은 윈도우에 해당하는 요소 값을 반환한다.
func (l Limits) Get(w Window) int {
	switch w {
	case Daily:
		return l.Daily
	case Weekly:
		return l.Weekly
	default:
		return l.Monthly
	}
}

// PeriodKeys 는 현재 시점의 윈도우별 기간 키다(어드민 표시용).
type PeriodKeys struct {
	Daily   string
	Weekly  string
	Monthly string
}

// Snapshot 은 어드민 표시용 읽기 모델이다(전역 기본값 + 멤버별 한도/사용량 + 기간 키).
type Snapshot struct {
	Default Limits
	Keys    PeriodKeys
	Limits  map[string]Limits // 멤버별 오버라이드(0 초과만)
	Usage   map[string]Limits // 멤버별 이번 일/주/월 사용량
}

// Repository — out 포트(사용량/한도 영속화).
type Repository interface {
	// AddUsage 는 member/period 사용량을 원자적으로 += tokens 한다(없으면 생성).
	AddUsage(ctx context.Context, memberID, period string, tokens int) error
	// GetUsage 는 member/period 누적 사용량을 반환한다(없으면 0).
	GetUsage(ctx context.Context, memberID, period string) (int, error)
	// UsageByPeriod 는 해당 period 전체 멤버 사용량 맵을 반환한다.
	UsageByPeriod(ctx context.Context, period string) (map[string]int, error)
	// ResetUsage 는 멤버의 모든 기간 사용량을 삭제(0으로 초기화)한다.
	ResetUsage(ctx context.Context, memberID string) error

	// MemberLimits 는 멤버별 일/주/월 한도를 반환한다(미설정=0).
	MemberLimits(ctx context.Context, memberID string) (Limits, error)
	// SetMemberLimits 는 멤버별 한도를 upsert 한다(0 = 전역 기본값 사용).
	SetMemberLimits(ctx context.Context, memberID string, l Limits) error
	// AllMemberLimits 는 하나라도 0 보다 큰 멤버별 한도 맵을 반환한다.
	AllMemberLimits(ctx context.Context) (map[string]Limits, error)

	// DefaultLimits 는 전역 기본 일/주/월 한도를 반환한다(0 = 무제한).
	DefaultLimits(ctx context.Context) (Limits, error)
	// SetDefaultLimits 는 전역 기본 한도를 저장한다.
	SetDefaultLimits(ctx context.Context, l Limits) error
}
