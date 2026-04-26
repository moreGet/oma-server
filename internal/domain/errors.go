package domain

import "errors"

var (
	ErrProviderNotFound    = errors.New("llm provider not found")
	ErrNoActiveProvider    = errors.New("no active llm provider")
	ErrInvalidProvider     = errors.New("invalid provider: name is required")
	ErrInvalidProviderType = errors.New("invalid provider type: must be LOCAL or EXTERNAL")
	ErrProviderConflict    = errors.New("provider already exists")
)
