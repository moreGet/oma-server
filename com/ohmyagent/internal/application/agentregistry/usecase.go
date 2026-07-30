// Package agentregistryapp 는 에이전트 레지스트리(등록·발견·생존성·A2A 토큰 브로커) 유스케이스를 담는다.
package agentregistryapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainagentregistry.Service = (*Service)(nil)

// 기본값(§공유 계약 권장값). config 미설정(0) 시 폴백.
const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultLeaseTTL          = 45 * time.Second // = 3×interval
	defaultTokenTTL          = 120 * time.Second
	// sweepOfflineRetention: sweeper 가 삭제하는 "offline 로 오래 방치된" 레코드의 보존 기간.
	// offline 문턱(3×lease_ttl≈2분)보다 훨씬 길게 잡아, 일시 중단된 에이전트가 재기동 시
	// 같은 (owner, name) 업서트로 agent_id 를 유지할 여지를 준다(발견 정확도는 read 시 status 계산이 보장).
	sweepOfflineRetention = 24 * time.Hour
)

// accessGate 는 admin 인가 게이트다(authUC 가 충족 — 의존성 역전).
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// Service 는 레지스트리 유스케이스다.
type Service struct {
	repo              domainagentregistry.Repository
	gate              accessGate
	directory         domainagentregistry.MemberDirectory // nil 허용(어드민 owner 이름 표시용)
	heartbeatInterval time.Duration
	leaseTTL          time.Duration
	now               func() time.Time

	broker *broker // A2A 토큰 브로커(EnableBroker 호출 전 nil — 브로커 엔드포인트만 비활성)
}

// NewService 는 레지스트리 유스케이스를 생성한다. interval/ttl 이 0 이하이면 권장 기본값을 쓴다.
func NewService(repo domainagentregistry.Repository, gate accessGate, heartbeatInterval, leaseTTL time.Duration) *Service {
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultHeartbeatInterval
	}
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}
	return &Service{
		repo:              repo,
		gate:              gate,
		heartbeatInterval: heartbeatInterval,
		leaseTTL:          leaseTTL,
		now:               time.Now,
	}
}

// SetMemberDirectory 는 어드민 표시용 owner 이름 해석기를 주입한다(선택).
func (s *Service) SetMemberDirectory(d domainagentregistry.MemberDirectory) { s.directory = d }

// LeaseTTL 은 리스 만료 시간을 반환한다(register/heartbeat 응답용).
func (s *Service) LeaseTTL() time.Duration { return s.leaseTTL }

// HeartbeatInterval 은 권장 heartbeat 주기를 반환한다(register 응답용).
func (s *Service) HeartbeatInterval() time.Duration { return s.heartbeatInterval }

// Register 는 (owner, name) 업서트로 에이전트를 등록한다.
// 재등록이면 기존 agent_id 를 유지하고 endpoint/capabilities 등을 갱신한다(재기동 시 중복 방지).
func (s *Service) Register(ctx context.Context, cmd domainagentregistry.RegisterCommand) (domainagentregistry.Agent, error) {
	if err := cmd.Validate(); err != nil {
		return domainagentregistry.Agent{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	a := domainagentregistry.Agent{
		ID:              uuid.NewString(), // 업서트 충돌 시 레포가 기존 id 로 대체
		OwnerID:         cmd.OwnerID,
		Name:            cmd.Name,
		EndpointURL:     cmd.EndpointURL,
		Capabilities:    cmd.Capabilities,
		Tags:            cmd.Tags,
		Model:           cmd.Model,
		Version:         cmd.Version,
		LastHeartbeatAt: now, // 등록 자체를 첫 heartbeat 으로 간주
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	saved, err := s.repo.Upsert(ctx, a)
	if err != nil {
		return domainagentregistry.Agent{}, err
	}
	saved.Status = domainagentregistry.StatusOnline
	return saved, nil
}

// Heartbeat 은 생존 신호를 기록한다.
// 없는 id → ErrNotFound, 존재하지만 소유자 불일치 → ErrForbidden(핸들러가 404 로 은닉 매핑).
func (s *Service) Heartbeat(ctx context.Context, id, ownerID string) error {
	err := s.repo.Touch(ctx, id, ownerID, s.now().UTC().Truncate(time.Second))
	if errors.Is(err, domainagentregistry.ErrNotFound) {
		return s.classifyOwnerMiss(ctx, id)
	}
	return err
}

// Deregister 는 우아한 해제다(소유자만).
func (s *Service) Deregister(ctx context.Context, id, ownerID string) error {
	err := s.repo.Delete(ctx, id, ownerID)
	if errors.Is(err, domainagentregistry.ErrNotFound) {
		return s.classifyOwnerMiss(ctx, id)
	}
	return err
}

// classifyOwnerMiss 는 (id, owner) 매칭 실패를 세분한다:
// id 자체가 없으면 ErrNotFound, 존재하는데 소유자가 다르면 ErrForbidden.
// (둘 다 HTTP 404 로 응답하지만, 감사 로그·테스트에서 구분 가치가 있다.)
func (s *Service) classifyOwnerMiss(ctx context.Context, id string) error {
	if _, err := s.repo.Get(ctx, id); err == nil {
		return domainagentregistry.ErrForbidden
	}
	return domainagentregistry.ErrNotFound
}

// Discover 는 필터 + 생존성 계산으로 에이전트를 발견한다.
// status 필터 기본값은 online+stale(offline 은닉), ?status= 지정 시 해당 상태만.
func (s *Service) Discover(ctx context.Context, f domainagentregistry.Filter) ([]domainagentregistry.Agent, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	// rows 의 배열을 재사용해 제자리에서 걸러낸다(AdminList 와 같은 방식).
	// 별도 out 슬라이스를 len(rows) 로 미리 잡으면 같은 데이터가 두 벌 동시에 살아 있게 된다 —
	// 레포가 방금 할당한 배열이라 다른 곳에서 참조하지 않으므로 덮어써도 안전하다.
	out := rows[:0]
	for _, a := range rows {
		if !a.Matches(f) { // 레포 SQL 은 코스 필터 — 최종 판정은 도메인 로직
			continue
		}
		a.Status = domainagentregistry.ComputeStatus(a.LastHeartbeatAt, now, s.leaseTTL)
		if !statusAllowed(a.Status, f.Status) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// statusAllowed 는 status 필터를 적용한다("" = online+stale 기본).
func statusAllowed(st domainagentregistry.Status, filter string) bool {
	if filter == "" {
		return st == domainagentregistry.StatusOnline || st == domainagentregistry.StatusStale
	}
	return string(st) == filter
}

// Get 은 단건 조회다(status 계산 포함).
func (s *Service) Get(ctx context.Context, id string) (domainagentregistry.Agent, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return domainagentregistry.Agent{}, err
	}
	a.Status = domainagentregistry.ComputeStatus(a.LastHeartbeatAt, s.now().UTC(), s.leaseTTL)
	return a, nil
}

// --- 어드민(/admin/agents) ---

// AdminList 는 전체 에이전트를 status·owner 이름과 함께 반환한다(admin 게이트, offline 포함).
func (s *Service) AdminList(ctx context.Context, actorID string) ([]domainagentregistry.Agent, error) {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := s.repo.List(ctx, domainagentregistry.Filter{})
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	ownerIDs := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		rows[i].Status = domainagentregistry.ComputeStatus(rows[i].LastHeartbeatAt, now, s.leaseTTL)
		if _, dup := seen[rows[i].OwnerID]; !dup {
			seen[rows[i].OwnerID] = struct{}{}
			ownerIDs = append(ownerIDs, rows[i].OwnerID)
		}
	}
	if s.directory != nil && len(ownerIDs) > 0 {
		names, err := s.directory.UsernamesByIDs(ctx, ownerIDs)
		if err != nil {
			// 이름 해석 실패는 목록 자체를 막지 않는다(id 로 폴백 표시).
			slog.Warn("agent registry: resolve owner names failed", "error", err)
		} else {
			for i := range rows {
				rows[i].OwnerName = names[rows[i].OwnerID]
			}
		}
	}
	return rows, nil
}

// AdminDelete 는 어드민 강제 해제다(소유자 무관, admin 게이트).
func (s *Service) AdminDelete(ctx context.Context, actorID, id string) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	return s.repo.DeleteByID(ctx, id)
}

// --- sweeper(선택 백그라운드 정리) ---

// RunSweeper 는 interval 주기로 offline 로 오래 방치된 레코드를 정리한다(레코드 무한 축적 방지).
// interval <= 0 이면 비활성. ctx 취소 시 종료. main 에서 go 로 띄운다.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := s.now().UTC().Add(-sweepOfflineRetention)
			n, err := s.repo.DeleteHeartbeatBefore(ctx, cutoff)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("agent registry sweep failed", "error", fmt.Errorf("sweep: %w", err))
				}
				continue
			}
			if n > 0 {
				slog.Info("agent registry sweep", "removed", n, "cutoff", cutoff)
			}
		}
	}
}
