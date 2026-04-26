package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/domain/port"
)

const (
	toolExecutionTimeout = 60 * time.Second
	sseChannelBuffer     = 32
)

type pendingCall struct {
	resultCh chan domain.ToolResult
}

// ClientManager는 다수의 C# 클라이언트를 관리하고
// SSE + JSON-RPC 2.0 으로 도구 실행 명령을 푸시합니다.
type ClientManager struct {
	mu       sync.RWMutex
	clients  map[string]*domain.Client       // clientID -> Client
	channels map[string]chan port.MCPCommand // clientID -> SSE 채널
	pending  map[string]pendingCall          // commandID -> 결과 대기 채널
	logger   *zap.Logger
}

func NewClientManager(logger *zap.Logger) *ClientManager {
	return &ClientManager{
		clients:  make(map[string]*domain.Client),
		channels: make(map[string]chan port.MCPCommand),
		pending:  make(map[string]pendingCall),
		logger:   logger,
	}
}

func (cm *ClientManager) RegisterClient(ctx context.Context, client *domain.Client) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	client.Status = domain.ClientStatusOnline
	client.ConnectedAt = now
	client.LastSeen = now
	cm.clients[client.ID] = client

	cm.logger.Info("client registered",
		zap.String("clientID", client.ID),
		zap.String("name", client.Name),
	)
	return nil
}

func (cm *ClientManager) GetOnlineClients(ctx context.Context) ([]*domain.Client, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	clients := make([]*domain.Client, 0, len(cm.clients))
	for _, c := range cm.clients {
		if c.Status == domain.ClientStatusOnline {
			clients = append(clients, c)
		}
	}
	return clients, nil
}

// SubscribeClient는 C# 클라이언트의 SSE 연결을 등록하고 명령 채널을 반환합니다.
func (cm *ClientManager) SubscribeClient(clientID string) (<-chan port.MCPCommand, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 기존 채널이 있으면 닫고 새로 생성
	if existing, ok := cm.channels[clientID]; ok {
		close(existing)
	}

	ch := make(chan port.MCPCommand, sseChannelBuffer)
	cm.channels[clientID] = ch

	if client, ok := cm.clients[clientID]; ok {
		client.Status = domain.ClientStatusOnline
		client.LastSeen = time.Now()
	}

	cm.logger.Info("client SSE subscribed", zap.String("clientID", clientID))
	return ch, nil
}

// UnsubscribeClient는 C# 클라이언트의 SSE 연결을 해제합니다.
func (cm *ClientManager) UnsubscribeClient(clientID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if ch, ok := cm.channels[clientID]; ok {
		close(ch)
		delete(cm.channels, clientID)
	}

	if client, ok := cm.clients[clientID]; ok {
		client.Status = domain.ClientStatusOffline
	}

	cm.logger.Info("client SSE unsubscribed", zap.String("clientID", clientID))
}

// ExecuteTool은 지정한 C# 클라이언트에 도구 실행을 요청하고 결과를 기다립니다.
func (cm *ClientManager) ExecuteTool(ctx context.Context, clientID string, toolCall domain.ToolCall) (*domain.ToolResult, error) {
	cm.mu.RLock()
	ch, connected := cm.channels[clientID]
	cm.mu.RUnlock()

	if !connected {
		return nil, fmt.Errorf("client %q is not connected", clientID)
	}

	commandID := uuid.New().String()
	resultCh := make(chan domain.ToolResult, 1)

	cm.mu.Lock()
	cm.pending[commandID] = pendingCall{resultCh: resultCh}
	cm.mu.Unlock()

	defer func() {
		cm.mu.Lock()
		delete(cm.pending, commandID)
		cm.mu.Unlock()
	}()

	cmd := port.MCPCommand{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]any{
			"tool_name":  toolCall.ToolName,
			"parameters": toolCall.Parameters,
		},
		ID: commandID,
	}

	// SSE 채널로 명령 전송
	select {
	case ch <- cmd:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, fmt.Errorf("client %q command channel is full", clientID)
	}

	cm.logger.Info("tool command sent",
		zap.String("clientID", clientID),
		zap.String("tool", toolCall.ToolName),
		zap.String("commandID", commandID),
	)

	// 결과 대기
	timeoutCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	select {
	case result := <-resultCh:
		return &result, nil
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("tool execution timed out after %s", toolExecutionTimeout)
	}
}

// PublishResult는 C# 클라이언트로부터 도구 실행 결과를 수신합니다.
func (cm *ClientManager) PublishResult(clientID string, result domain.ToolResult) error {
	cm.mu.Lock()
	pending, ok := cm.pending[result.ToolCallID]
	if ok {
		if client, exists := cm.clients[clientID]; exists {
			client.LastSeen = time.Now()
		}
	}
	cm.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending command with ID %q", result.ToolCallID)
	}

	pending.resultCh <- result
	return nil
}
