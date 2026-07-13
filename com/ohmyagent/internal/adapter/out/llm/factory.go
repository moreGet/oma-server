// Package llm 은 LLM Provider 의 out 포트(Factory/Cache/Adapter) 구현을 담는다.
package llm

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// newSharedHTTPClient 는 업스트림 TCP/TLS 커넥션을 재사용하는 공유 HTTP 클라이언트를 만든다(기본 Transport 의 MaxIdleConnsPerHost=2 병목 제거).
// 전역 타임아웃은 두지 않는다(스트리밍이 길 수 있어 취소는 ctx 로 제어).
func newSharedHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// resolveAPIKey 는 어댑터의 API 키를 해석한다.
// 직접 저장된 키(apiKey, 유스케이스가 복호화한 평문)를 우선하고, 없으면 환경변수(apiKeyEnv)에서 읽는다.
func resolveAPIKey(apiKey, apiKeyEnv string) string {
	if apiKey != "" {
		return apiKey
	}
	if apiKeyEnv != "" {
		return os.Getenv(apiKeyEnv)
	}
	return ""
}

// EXTERNAL Provider 를 모델명 접두사로 어댑터에 분기한다(claude*→Claude, gemini*→Gemini, 그 외→OpenAI).
const (
	claudeModelPrefix = "claude"
	geminiModelPrefix = "gemini"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Factory = (*Factory)(nil)

// Factory 는 도메인 LLMProvider → Adapter 분기 생성을 담당한다.
// httpClient 는 모든 외부 어댑터가 공유하는 커넥션 풀이다(요청마다 재생성하지 않는다).
type Factory struct {
	httpClient *http.Client
}

// NewFactory 는 Factory 구현체를 반환한다(공유 HTTP 클라이언트 1회 생성).
func NewFactory() *Factory { return &Factory{httpClient: newSharedHTTPClient()} }

// CreateAdapter 는 ProviderType 에 따라 어댑터를 생성한다.
// LOCAL→Ollama, EXTERNAL 은 model 접두사로 claude→Claude / gemini→Gemini / 그 외→OpenAI 분기.
func (f *Factory) CreateAdapter(provider domainllmprovider.LLMProvider) (domainllmprovider.Adapter, error) {
	switch provider.ProviderType {
	case domainllmprovider.ProviderTypeLocal:
		return NewOllamaAdapter(provider.Config, f.httpClient), nil
	case domainllmprovider.ProviderTypeExternal:
		model := strings.ToLower(provider.Config.Model)
		switch {
		case strings.HasPrefix(model, claudeModelPrefix):
			return NewClaudeAdapter(provider.Config, f.httpClient), nil
		case strings.HasPrefix(model, geminiModelPrefix):
			return NewGeminiAdapter(provider.Config, f.httpClient), nil
		default:
			return NewOpenAIAdapter(provider.Config, f.httpClient), nil
		}
	default:
		return nil, fmt.Errorf("unknown provider type: %s", provider.ProviderType)
	}
}
