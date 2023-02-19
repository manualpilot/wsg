package internal

import (
	"sync"
)

type Message struct {
	Drop   bool
	Binary bool
	Buffer []byte
}

type State struct {
	Lock        sync.RWMutex
	Connections map[string]chan Message
}

type EventType string

const (
	EventTypeWrite EventType = "write"
	EventTypeDrop  EventType = "drop"
)

type Event struct {
	Type    EventType `json:"type"`
	ID      string    `json:"id"`
	Binary  bool      `json:"binary"`
	Payload string    `json:"payload"`
}
