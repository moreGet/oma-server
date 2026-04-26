package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// OllamaAdapter는 로컬 Ollama 서버(Gemma)와 통신하는 아웃바운드 어댑터입니다.
type OllamaAdapter struct {
	baseURL string
	model   string
	client  *http.Client
	logger  *zap.Logger
}

func NewOllamaAdapter(baseURL, model string, logger *zap.Logger) *OllamaAdapter {
	return &OllamaAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
		logger:  logger,
	}
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

func (a *OllamaAdapter) Chat(ctx context.Context, messages []domain.Message, tools []domain.Tool) (*domain.Message, error) {
	ollamaMessages := make([]ollamaMessage, 0, len(messages))
	for _, msg := range messages {
		content := msg.Content
		role := string(msg.Role)

		// 도구 결과는 user 역할로 전달 (Ollama는 tool 역할 미지원)
		if msg.Role == domain.RoleTool && msg.ToolResult != nil {
			role = "user"
			if msg.ToolResult.Error != "" {
				content = fmt.Sprintf("[도구 실행 오류]\n%s", msg.ToolResult.Error)
			} else {
				content = fmt.Sprintf("[도구 실행 결과]\n%s", msg.ToolResult.Output)
			}
		}

		ollamaMessages = append(ollamaMessages, ollamaMessage{Role: role, Content: content})
	}

	reqBody := ollamaChatRequest{
		Model:    a.model,
		Messages: ollamaMessages,
		Stream:   false,
		Options: map[string]any{
			"temperature": 0.3,
			"num_predict": 2048,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	a.logger.Debug("ollama request", zap.String("model", a.model), zap.Int("messages", len(ollamaMessages)))

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, string(body))
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	content := strings.TrimSpace(ollamaResp.Message.Content)
	previewLen := len(content)
	if previewLen > 200 {
		previewLen = 200
	}
	a.logger.Debug("ollama response", zap.String("content", content[:previewLen]))

	return &domain.Message{
		ID:        uuid.New().String(),
		Role:      domain.RoleAssistant,
		Content:   content,
		CreatedAt: time.Now(),
	}, nil
}
