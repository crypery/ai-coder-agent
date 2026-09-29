package session

import (
	"crypto/rand"
	"fmt"
)

// Session represents a conversation session with metadata.
type Session struct {
	ID            string
	ParentSession string
	AgentName     string
	Mode          string
}

// New creates a new session with a unique ID.
func New(agentName, mode string) *Session {
	return &Session{
		ID:        generateID(),
		AgentName: agentName,
		Mode:      mode,
	}
}

// WithParent returns a new session with the given parent session ID.
func (s *Session) WithParent(parentID string) *Session {
	return &Session{
		ID:            generateID(),
		ParentSession: parentID,
		AgentName:     s.AgentName,
		Mode:          s.Mode,
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
