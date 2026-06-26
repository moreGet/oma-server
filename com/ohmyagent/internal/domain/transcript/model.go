// Package transcript 는 대화 이력(요청 + 누적 응답 + 메타데이터) 영속화 어그리거트의
// 도메인 모델·포트를 담는다. 본문은 저장 백엔드에서 gzip 압축되어 보관된다.
package transcript

import "time"

// Source 는 대화 이력의 출처다.
type Source string

const (
	SourceChat  Source = "chat"  // POST /api/v1/chat
	SourceAgent Source = "agent" // POST /api/v1/agent/chat
)

// Transcript 는 한 번의 LLM 질의 기록이다.
// Request 는 요청 메시지/도구 JSON, Response 는 누적된 assistant 텍스트다.
type Transcript struct {
	ID               string
	MemberID         string
	SessionID        string // 선택(agent 세션 등)
	Source           Source
	Model            string
	Request          []byte // 요청 페이로드 JSON
	Response         string // 누적 응답 텍스트
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	FinishReason     string
	CreatedAt        time.Time
}
