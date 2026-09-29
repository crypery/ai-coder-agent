package session

import "testing"

func TestNewSessionHasIDAndMetadata(t *testing.T) {
	s := New("build", "primary")
	if s.ID == "" {
		t.Error("session id must not be empty")
	}
	if s.AgentName != "build" || s.Mode != "primary" {
		t.Errorf("metadata = %q/%q", s.AgentName, s.Mode)
	}
}

func TestSessionsHaveUniqueIDs(t *testing.T) {
	a := New("build", "primary")
	b := New("build", "primary")
	if a.ID == b.ID {
		t.Error("session ids must be unique")
	}
}

func TestWithParent(t *testing.T) {
	s := New("build", "primary")
	child := s.WithParent(s.ID)
	if child.ID == s.ID {
		t.Error("child session id must differ")
	}
	if child.ParentSession != s.ID {
		t.Errorf("parent = %q, want %q", child.ParentSession, s.ID)
	}
	if child.AgentName != s.AgentName || child.Mode != s.Mode {
		t.Error("child must inherit agent metadata")
	}
}