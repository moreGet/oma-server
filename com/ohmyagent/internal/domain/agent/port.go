package agent

import "context"

// Service — in 포트(유스케이스가 노출하는 능력).
// Stream 은 활성 Provider 의 LLM 으로 메시지+도구를 전달하고, 응답 이벤트(content_delta/tool_call/message_stop)를
// onEvent 로 순차 전달한다. onEvent 가 에러를 반환하면 중단한다.
type Service interface {
	Stream(ctx context.Context, cmd ChatCommand, onEvent func(Event) error) error
}
