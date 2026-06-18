package chat

import "context"

// Service — in 포트(유스케이스가 노출하는 능력).
// Stream 은 활성 Provider 로 질의를 전달하고, 응답 조각을 onChunk 로 순차 전달한다.
// 마지막에 Done=true 조각이 1회 전달된다. onChunk 가 에러를 반환하면 중단한다.
type Service interface {
	Stream(ctx context.Context, cmd ChatCommand, onChunk func(StreamChunk) error) error
}
