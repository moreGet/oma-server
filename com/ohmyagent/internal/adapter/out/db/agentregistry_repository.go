package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainagentregistry.Repository = (*AgentRegistryRepository)(nil)

// AgentRegistryRepository 는 에이전트 레지스트리 레코드를 영속화한다.
// (owner_member_id, name) 유니크 업서트는 driver 별 atomic SQL 로 처리한다.
type AgentRegistryRepository struct {
	db     *sql.DB
	driver string // mysql | sqlite (upsert SQL 분기)
}

// NewAgentRegistryRepository 는 AgentRegistryRepository 를 생성한다.
func NewAgentRegistryRepository(conn *sql.DB, driver string) *AgentRegistryRepository {
	return &AgentRegistryRepository{db: conn, driver: driver}
}

const agentColumns = "id, owner_member_id, name, endpoint_url, capabilities, tags, model, version, last_heartbeat_at, created_at, updated_at"

// Upsert 는 (owner_member_id, name) 기준 driver 별 atomic upsert 다(동시 재등록 레이스 제거).
// 충돌 시 id/created_at 은 최초 INSERT 값을 유지하고, 확정 레코드(기존 id 포함)를 reload 해 반환한다.
func (r *AgentRegistryRepository) Upsert(ctx context.Context, a domainagentregistry.Agent) (domainagentregistry.Agent, error) {
	caps, err := marshalStringList(a.Capabilities)
	if err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("agent registry: marshal capabilities: %w", err)
	}
	tags, err := marshalStringList(a.Tags)
	if err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("agent registry: marshal tags: %w", err)
	}
	tail := " ON CONFLICT(owner_member_id, name) DO UPDATE SET endpoint_url=excluded.endpoint_url, capabilities=excluded.capabilities, tags=excluded.tags, model=excluded.model, version=excluded.version, last_heartbeat_at=excluded.last_heartbeat_at, updated_at=excluded.updated_at" // sqlite
	if r.driver == "mysql" {
		tail = " ON DUPLICATE KEY UPDATE endpoint_url=VALUES(endpoint_url), capabilities=VALUES(capabilities), tags=VALUES(tags), model=VALUES(model), version=VALUES(version), last_heartbeat_at=VALUES(last_heartbeat_at), updated_at=VALUES(updated_at)"
	}
	q := "INSERT INTO agents (" + agentColumns + ") VALUES (?,?,?,?,?,?,?,?,?,?,?)" + tail
	if _, err := r.db.ExecContext(ctx, q,
		a.ID, a.OwnerID, a.Name, a.EndpointURL, caps, tags, a.Model, a.Version,
		a.LastHeartbeatAt.Unix(), a.CreatedAt.Unix(), a.UpdatedAt.Unix(),
	); err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("agent registry: upsert: %w", err)
	}
	// 확정 행 reload: 재등록이면 기존 id/created_at 이 유지된다.
	row := r.db.QueryRowContext(ctx, "SELECT "+agentColumns+" FROM agents WHERE owner_member_id=? AND name=?", a.OwnerID, a.Name)
	saved, err := scanAgent(row)
	if err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("agent registry: upsert reload: %w", err)
	}
	return saved, nil
}

// Get 은 id 단건 조회다. 없으면 도메인 ErrNotFound.
func (r *AgentRegistryRepository) Get(ctx context.Context, id string) (domainagentregistry.Agent, error) {
	return queryOne(ctx, r.db, "agent registry: get", domainagentregistry.ErrNotFound, scanAgent,
		"SELECT "+agentColumns+" FROM agents WHERE id=?", id)
}

// Touch 는 heartbeat 시각을 갱신한다. (id, ownerID) 매칭 행이 없으면 ErrNotFound.
// 주의: mysql 드라이버의 RowsAffected 는 "값이 실제 바뀐 행" 수라(CLIENT_FOUND_ROWS 미사용),
// 같은 초에 두 번 heartbeat 하면 0 이 온다 — 존재 재확인으로 위양성 404 를 막는다.
func (r *AgentRegistryRepository) Touch(ctx context.Context, id, ownerID string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE agents SET last_heartbeat_at=?, updated_at=? WHERE id=? AND owner_member_id=?",
		now.Unix(), now.Unix(), id, ownerID)
	if err != nil {
		return fmt.Errorf("agent registry: touch: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	err = r.db.QueryRowContext(ctx, "SELECT 1 FROM agents WHERE id=? AND owner_member_id=?", id, ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return domainagentregistry.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("agent registry: touch verify: %w", err)
	}
	return nil // 존재하지만 동일 값 갱신(같은 초 재-heartbeat) — 성공으로 처리
}

// Delete 는 소유자 스코프 삭제다. 매칭 행이 없으면 ErrNotFound.
func (r *AgentRegistryRepository) Delete(ctx context.Context, id, ownerID string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM agents WHERE id=? AND owner_member_id=?", id, ownerID)
	if err != nil {
		return fmt.Errorf("agent registry: delete: %w", err)
	}
	return affectedOrNotFound(res, domainagentregistry.ErrNotFound)
}

// DeleteByID 는 소유자 무관 삭제다(어드민 강제 해제). 없으면 ErrNotFound.
func (r *AgentRegistryRepository) DeleteByID(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM agents WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("agent registry: delete by id: %w", err)
	}
	return affectedOrNotFound(res, domainagentregistry.ErrNotFound)
}

// DeleteHeartbeatBefore 는 cutoff 이전 heartbeat 레코드를 일괄 삭제한다(sweeper).
func (r *AgentRegistryRepository) DeleteHeartbeatBefore(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM agents WHERE last_heartbeat_at < ?", cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("agent registry: sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// List 는 코스 필터를 SQL where 로 적용해 조회한다(이름순).
// capability/tag/query 는 json text LIKE 프리필터라 위양성이 가능하며(위음성은 없음),
// 최종 정밀 판정·status 계산은 유스케이스(도메인 Matches/ComputeStatus)가 수행한다.
func (r *AgentRegistryRepository) List(ctx context.Context, f domainagentregistry.Filter) ([]domainagentregistry.Agent, error) {
	q := "SELECT " + agentColumns + " FROM agents"
	var conds []string
	var args []any
	if f.OwnerID != "" {
		conds = append(conds, "owner_member_id=?")
		args = append(args, f.OwnerID)
	}
	if f.ExcludeID != "" {
		conds = append(conds, "id<>?")
		args = append(args, f.ExcludeID)
	}
	// LIKE 와일드카드(%/_)나 json 이스케이프 대상(" \\)이 섞인 값은 프리필터를 생략한다
	// (좁히면 위음성 위험 — 앱단 정밀 필터가 어차피 최종 판정).
	if p, ok := jsonElemLike(f.Capability); ok {
		conds = append(conds, "capabilities LIKE ?")
		args = append(args, p)
	}
	if p, ok := jsonElemLike(f.Tag); ok {
		conds = append(conds, "tags LIKE ?")
		args = append(args, p)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY name"
	return queryList(ctx, r.db, "agent registry: list", scanAgent, q, args...)
}

// jsonElemLike 는 json 배열 원소 정확 일치용 LIKE 패턴(`%"v"%`)을 만든다.
// 값에 LIKE/json 특수문자가 있으면 프리필터 불가(false)로 알린다.
func jsonElemLike(v string) (string, bool) {
	if v == "" || strings.ContainsAny(v, `%_"\`) {
		return "", false
	}
	return `%"` + v + `"%`, true
}

// marshalStringList 는 태그 목록을 json text 로 직렬화한다(nil → "[]").
func marshalStringList(v []string) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// scanAgent 는 agentColumns 순서로 한 행을 도메인 엔티티로 복원한다.
func scanAgent(sc rowScanner) (domainagentregistry.Agent, error) {
	var a domainagentregistry.Agent
	var caps, tags string
	var hbUnix, createdUnix, updatedUnix int64
	if err := sc.Scan(&a.ID, &a.OwnerID, &a.Name, &a.EndpointURL, &caps, &tags, &a.Model, &a.Version, &hbUnix, &createdUnix, &updatedUnix); err != nil {
		return domainagentregistry.Agent{}, err
	}
	if err := json.Unmarshal([]byte(caps), &a.Capabilities); err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("unmarshal capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(tags), &a.Tags); err != nil {
		return domainagentregistry.Agent{}, fmt.Errorf("unmarshal tags: %w", err)
	}
	if hbUnix > 0 {
		a.LastHeartbeatAt = time.Unix(hbUnix, 0).UTC()
	}
	a.CreatedAt = time.Unix(createdUnix, 0).UTC()
	a.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return a, nil
}
