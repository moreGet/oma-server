package transcript

import (
	"context"
	"time"
)

// Store — out 포트(영속화 백엔드). 구현: DB gzip BLOB / 로컬 파일 / S3.
// 어드민 설정으로 백엔드를 교체할 수 있도록 인터페이스로 추상화한다.
type Store interface {
	Save(ctx context.Context, t Transcript) error
}

// Recorder — in 포트. 핸들러가 호출하는 **비차단** 기록기로, 요청 핫패스를 막지 않는다.
// 구현은 버퍼 채널 + 백그라운드 워커로 Store 에 위임한다(버퍼 풀이면 드롭).
type Recorder interface {
	Record(t Transcript)
}

// Purger — 보존 정책(TTL) 지원 Store 가 선택적으로 구현하는 포트.
// olderThan 이전 이력을 삭제하고 삭제 건수를 반환한다(DB/파일). S3 는 라이프사이클로 처리.
type Purger interface {
	Purge(ctx context.Context, olderThan time.Time) (int, error)
}
