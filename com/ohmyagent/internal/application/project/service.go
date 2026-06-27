// Package projectapp 는 클라이언트 동기화용 프로젝트/대화(세션) 유스케이스를 담는다.
// 메타데이터는 repo, 대화 본문은 gzip 후 ContentStore 에 저장. 신규 세션은 계정별 캡(하드)으로 제한한다.
package projectapp

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

// gzipWriterPool 은 gzip.Writer 를 재사용한다(대화 본문 저장이 업서트마다 발생 → 매번 할당 시 GC 압력).
var gzipWriterPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// maxSessionsFunc 는 멤버의 유효 최대 세션 수를 반환한다(0 = 무제한). sessionapp.Manager.EffectiveMaxSessions.
type maxSessionsFunc func(ctx context.Context, memberID string) (int, error)

// Service 는 프로젝트/대화 동기화 서비스다.
type Service struct {
	projects    domainproject.ProjectRepository
	convs       domainproject.ConversationRepository
	content     domainproject.ContentStore
	maxSessions maxSessionsFunc
	now         func() time.Time
}

func NewService(projects domainproject.ProjectRepository, convs domainproject.ConversationRepository, content domainproject.ContentStore, maxSessions maxSessionsFunc) *Service {
	return &Service{projects: projects, convs: convs, content: content, maxSessions: maxSessions, now: time.Now}
}

// UpsertProject 는 client_id 기준으로 프로젝트를 생성/갱신한다.
func (s *Service) UpsertProject(ctx context.Context, cmd domainproject.UpsertProjectCommand) (domainproject.Project, error) {
	cmd.Normalize()
	if err := cmd.Validate(); err != nil {
		return domainproject.Project{}, err
	}
	now := s.now().UTC()
	return s.projects.UpsertProject(ctx, domainproject.Project{
		ID: uuid.NewString(), OwnerID: cmd.OwnerID, ClientID: cmd.ClientID, Name: cmd.Name,
		CreatedUTC: now, UpdatedUTC: now,
	})
}

// ListProjects 는 소유 프로젝트 목록을 반환한다.
func (s *Service) ListProjects(ctx context.Context, ownerID string) ([]domainproject.Project, error) {
	return s.projects.ListProjects(ctx, ownerID)
}

// GetProject 는 프로젝트 단건 + 대화 요약 목록을 반환한다.
func (s *Service) GetProject(ctx context.Context, ownerID, id string) (domainproject.Project, []domainproject.Conversation, error) {
	p, err := s.projects.GetProject(ctx, ownerID, id)
	if err != nil {
		return domainproject.Project{}, nil, err
	}
	convs, err := s.convs.ListByProject(ctx, ownerID, id)
	if err != nil {
		return domainproject.Project{}, nil, err
	}
	return p, convs, nil
}

// DeleteProject 는 프로젝트(및 소속 대화 메타)를 삭제한다.
func (s *Service) DeleteProject(ctx context.Context, ownerID, id string) error {
	return s.projects.DeleteProject(ctx, ownerID, id)
}

// UpsertConversation 은 대화를 업서트하고 본문을 저장한다. 신규 세션은 캡 초과 시 ErrSessionLimitExceeded.
func (s *Service) UpsertConversation(ctx context.Context, cmd domainproject.UpsertConversationCommand) (domainproject.Conversation, error) {
	cmd.Normalize()
	if err := cmd.Validate(); err != nil {
		return domainproject.Conversation{}, err
	}
	// 프로젝트 지정 시 소유 검증.
	if cmd.ProjectID != "" {
		if _, err := s.projects.GetProject(ctx, cmd.OwnerID, cmd.ProjectID); err != nil {
			return domainproject.Conversation{}, err
		}
	}
	existingID, exists, err := s.convs.FindIDByClient(ctx, cmd.OwnerID, cmd.ClientID)
	if err != nil {
		return domainproject.Conversation{}, err
	}
	if !exists { // 신규 세션만 캡 적용(기존 업서트는 허용)
		max, err := s.maxSessions(ctx, cmd.OwnerID)
		if err != nil {
			return domainproject.Conversation{}, err
		}
		if max > 0 {
			count, err := s.convs.CountByOwner(ctx, cmd.OwnerID)
			if err != nil {
				return domainproject.Conversation{}, err
			}
			if count >= max {
				return domainproject.Conversation{}, domainproject.ErrSessionLimitExceeded
			}
		}
	}

	id := existingID
	if id == "" {
		id = uuid.NewString()
	}
	now := s.now().UTC()
	created := cmd.CreatedUTC
	if created.IsZero() {
		created = now
	}
	updated := cmd.UpdatedUTC
	if updated.IsZero() {
		updated = now
	}
	conv := domainproject.Conversation{
		ID: id, ProjectID: cmd.ProjectID, OwnerID: cmd.OwnerID, ClientID: cmd.ClientID, Title: cmd.Title,
		CreatedUTC: created, UpdatedUTC: updated, MessageCount: cmd.MessageCount,
	}
	// 본문 gzip 저장(디렉터리 구조 key).
	gz, err := gzipBytes(cmd.Messages)
	if err != nil {
		return domainproject.Conversation{}, fmt.Errorf("gzip messages: %w", err)
	}
	if err := s.content.Save(ctx, contentKey(conv), gz); err != nil {
		return domainproject.Conversation{}, fmt.Errorf("save content: %w", err)
	}
	return s.convs.UpsertConversation(ctx, conv)
}

// DeleteConversation 은 대화 메타데이터를 삭제한다.
func (s *Service) DeleteConversation(ctx context.Context, ownerID, id string) error {
	return s.convs.DeleteConversation(ctx, ownerID, id)
}

// contentKey 는 본문 저장 키를 만든다: "<owner>/<project|_unfiled>/<conv>.json.gz".
func contentKey(c domainproject.Conversation) string {
	proj := c.ProjectID
	if proj == "" {
		proj = "_unfiled"
	}
	return fmt.Sprintf("%s/%s/%s.json.gz", c.OwnerID, proj, c.ID)
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzipWriterPool.Get().(*gzip.Writer)
	defer gzipWriterPool.Put(zw)
	zw.Reset(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
