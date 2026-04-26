package domain

import "time"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

type Message struct {
	ID         string
	Role       Role
	Content    string
	ToolCall   *ToolCall
	ToolResult *ToolResult
	CreatedAt  time.Time
}

type ToolCall struct {
	ID         string
	ToolName   string
	Parameters map[string]any
}

type ToolResult struct {
	ToolCallID string
	Output     string
	Error      string
}
