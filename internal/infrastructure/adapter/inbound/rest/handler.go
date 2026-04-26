package rest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/domain/port"
)

type Handler struct {
	agent  port.AgentUseCase
	mcp    port.MCPClientPort
	logger *zap.Logger
}

func NewHandler(agent port.AgentUseCase, mcp port.MCPClientPort, logger *zap.Logger) *Handler {
	return &Handler{agent: agent, mcp: mcp, logger: logger}
}

// POST /api/sessions
func (h *Handler) CreateSession(c *gin.Context) {
	var req struct {
		ClientID string `json:"client_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	session, err := h.agent.StartSession(c.Request.Context(), req.ClientID)
	if err != nil {
		h.logger.Error("create session", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"session_id": session.ID,
		"client_id":  session.ClientID,
		"created_at": session.CreatedAt,
	})
}

// GET /api/sessions/:id
func (h *Handler) GetSession(c *gin.Context) {
	session, err := h.agent.GetSession(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"session_id":  session.ID,
		"client_id":   session.ClientID,
		"message_cnt": len(session.Messages),
		"updated_at":  session.UpdatedAt,
	})
}

// POST /api/chat
func (h *Handler) Chat(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
		ClientID  string `json:"client_id"  binding:"required"`
		Message   string `json:"message"    binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.logger.Info("chat request",
		zap.String("sessionID", req.SessionID),
		zap.String("clientID", req.ClientID),
	)

	response, err := h.agent.RunAgenticLoop(c.Request.Context(), req.SessionID, req.ClientID, req.Message)
	if err != nil {
		h.logger.Error("agentic loop", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id": req.SessionID,
		"response":   response,
	})
}

// POST /api/clients/register
func (h *Handler) RegisterClient(c *gin.Context) {
	var req struct {
		ID       string            `json:"id"       binding:"required"`
		Name     string            `json:"name"     binding:"required"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client := &domain.Client{
		ID:       req.ID,
		Name:     req.Name,
		Metadata: req.Metadata,
	}
	if err := h.mcp.RegisterClient(c.Request.Context(), client); err != nil {
		h.logger.Error("register client", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to register client"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"client_id": client.ID, "status": "registered"})
}

// GET /api/clients
func (h *Handler) ListClients(c *gin.Context) {
	clients, err := h.mcp.GetOnlineClients(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list clients"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"clients": clients})
}

// GET /api/clients/:id/events — SSE 스트림 (C# 클라이언트 전용)
func (h *Handler) ClientEvents(c *gin.Context) {
	clientID := c.Param("id")

	ch, err := h.mcp.SubscribeClient(clientID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer h.mcp.UnsubscribeClient(clientID)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	h.logger.Info("SSE stream opened", zap.String("clientID", clientID))

	// 연결 확인 이벤트
	fmt.Fprintf(c.Writer, "event: connected\ndata: {\"client_id\":%q}\n\n", clientID)
	c.Writer.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case cmd, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(cmd)
			if err != nil {
				h.logger.Error("marshal SSE command", zap.Error(err))
				continue
			}
			fmt.Fprintf(c.Writer, "event: command\ndata: %s\n\n", string(data))
			c.Writer.Flush()

		case <-heartbeat.C:
			fmt.Fprintf(c.Writer, "event: ping\ndata: {\"ts\":%d}\n\n", time.Now().Unix())
			c.Writer.Flush()

		case <-c.Request.Context().Done():
			h.logger.Info("SSE stream closed", zap.String("clientID", clientID))
			return
		}
	}
}

// POST /api/clients/:id/result — C# 클라이언트가 도구 실행 결과를 반환
func (h *Handler) ClientResult(c *gin.Context) {
	clientID := c.Param("id")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}

	// JSON-RPC 2.0 응답 형식
	var rpcResult struct {
		ID     string `json:"id"` // commandID
		Result struct {
			Output string `json:"output"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResult); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON-RPC response"})
		return
	}

	result := domain.ToolResult{ToolCallID: rpcResult.ID, Output: rpcResult.Result.Output}
	if rpcResult.Error != nil {
		result.Error = rpcResult.Error.Message
	}

	if err := h.mcp.PublishResult(clientID, result); err != nil {
		h.logger.Warn("publish result", zap.Error(err), zap.String("clientID", clientID))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
