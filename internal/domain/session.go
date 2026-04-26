package domain

import "time"

type ClientStatus string

const (
	ClientStatusOnline  ClientStatus = "online"
	ClientStatusOffline ClientStatus = "offline"
)

type Client struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Status      ClientStatus      `json:"status"`
	ConnectedAt time.Time         `json:"connected_at"`
	LastSeen    time.Time         `json:"last_seen"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type Session struct {
	ID        string
	ClientID  string
	Messages  []Message
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewSession(id, clientID string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		ClientID:  clientID,
		Messages:  []Message{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *Session) AddMessage(msg Message) {
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
}
