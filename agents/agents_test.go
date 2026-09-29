package agents

import (
	"strings"
	"testing"
)

func TestLoadBuiltInRegistersAgents(t *testing.T) {
	m := NewManager()
	m.LoadBuiltIn()
	for _, name := range []string{"build", "plan", "general", "explore", "scout", "code-reviewer"} {
		if _, ok := m.agents[name]; !ok {
			t.Errorf("agent %q not registered", name)
		}
	}
}

func TestSetCurrentAndGetCurrent(t *testing.T) {
	m := NewManager()
	m.LoadBuiltIn()
	if err := m.SetCurrent("explore"); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if got := m.GetCurrent().Name; got != "explore" {
		t.Errorf("current = %q, want explore", got)
	}
	if err := m.SetCurrent("missing"); err == nil {
		t.Error("expected error for unknown agent")
	}
}

func TestGetDefault(t *testing.T) {
	m := NewManager()
	m.LoadBuiltIn()
	if got := m.GetDefault().Name; got != "build" {
		t.Errorf("default = %q, want build", got)
	}
}

func TestGetSubagentNames(t *testing.T) {
	m := NewManager()
	m.LoadBuiltIn()
	names := m.GetSubagentNames()
	found := false
	for _, n := range names {
		if n == "general" {
			found = true
		}
	}
	if !found {
		t.Errorf("subagent names %v should include general", names)
	}
	for _, n := range names {
		if n == "build" {
			t.Errorf("primary agent build must not be listed as subagent")
		}
	}
}

func TestParseMarkdownAgent(t *testing.T) {
	content := `---
description: Test agent
mode: subagent
temperature: 0.5
websearch_limit: 2
disable: true
permission:
  read: allow
  edit: deny
---
This is the system prompt body.`
	a := parseMarkdownAgent(content, "test-agent")
	if a.Description != "Test agent" {
		t.Errorf("description = %q", a.Description)
	}
	if a.Mode != "subagent" {
		t.Errorf("mode = %q", a.Mode)
	}
	if a.Temperature != 0.5 {
		t.Errorf("temperature = %v", a.Temperature)
	}
	if a.WebSearchLimit != 2 {
		t.Errorf("websearch_limit = %d", a.WebSearchLimit)
	}
	if !a.Disabled {
		t.Error("disable should be true")
	}
	if a.Permissions["read"] != "allow" || a.Permissions["edit"] != "deny" {
		t.Errorf("permissions = %v", a.Permissions)
	}
	if !strings.Contains(a.SystemPrompt, "system prompt body") {
		t.Errorf("prompt = %q", a.SystemPrompt)
	}
}