// Package chatapp 는 채팅 질의 유스케이스를 담는다.
// 활성 Provider 어댑터를 resolver 로 얻어 스트리밍 질의를 위임하고, 도메인 타입을 매핑한다.
package chatapp

import (
	"context"

	domainchat "aiagent/com/ohmyagent/internal/domain/chat"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainchat.Service = (*ChatService)(nil)

// providerResolver 는 llmprovider 도메인을 직접 의존하지 않고 활성 어댑터만 얻기 위한
// 소비자 측 최소 인터페이스다(스펙 §4.4 의존성 역전). main.go 에서 ProviderService 를 주입한다.
type providerResolver interface {
	GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error)
}

// ChatService 는 domainchat.Service 구현이다.
type ChatService struct {
	providers providerResolver
}

// NewChatService 는 활성 어댑터 resolver 를 주입받아 ChatService 를 생성한다.
func NewChatService(providers providerResolver) *ChatService {
	return &ChatService{providers: providers}
}

// Stream 은 입력을 검증하고 활성 어댑터로 스트리밍 질의를 위임한다.
// 어댑터 조각(llmprovider 타입)을 chat 도메인 타입으로 매핑해 onChunk 로 전달한다.
func (s *ChatService) Stream(ctx context.Context, cmd domainchat.ChatCommand, onChunk func(domainchat.StreamChunk) error) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	adapter, err := s.providers.GetActiveAdapter(ctx)
	if err != nil {
		return err // ErrNoActiveProvider 등은 핸들러가 헤더 쓰기 전에 매핑.
	}
	req := toAdapterRequest(cmd)
	return adapter.ChatStream(ctx, req, func(c domainllmprovider.ChatStreamChunk) error {
		return onChunk(fromAdapterChunk(c))
	})
}

// toAdapterRequest 는 chat 커맨드를 llmprovider 어댑터 요청으로 매핑한다.
func toAdapterRequest(cmd domainchat.ChatCommand) domainllmprovider.ChatRequest {
	msgs := make([]domainllmprovider.ChatMessage, 0, len(cmd.Messages))
	for _, m := range cmd.Messages {
		msgs = append(msgs, domainllmprovider.ChatMessage{
			Role:    domainllmprovider.ChatRole(m.Role),
			Content: m.Content,
		})
	}
	return domainllmprovider.ChatRequest{
		Messages:    msgs,
		Model:       cmd.Model,
		MaxTokens:   cmd.MaxTokens,
		Temperature: cmd.Temperature,
	}
}

// fromAdapterChunk 는 어댑터 조각을 chat 도메인 조각으로 매핑한다.
func fromAdapterChunk(c domainllmprovider.ChatStreamChunk) domainchat.StreamChunk {
	out := domainchat.StreamChunk{
		Delta:        c.Delta,
		FinishReason: c.FinishReason,
		Done:         c.Done,
	}
	if c.Usage != nil {
		out.Usage = &domainchat.Usage{
			PromptTokens:     c.Usage.PromptTokens,
			CompletionTokens: c.Usage.CompletionTokens,
			TotalTokens:      c.Usage.TotalTokens,
		}
	}
	return out
}
