package context

import (
	"strings"
	"testing"

	"ai-coder-agent/session"
)

func TestBuildIncludesSections(t *testing.T) {
	s := session.New("build", "primary")
	b := NewBuilder().
		SetSession(s).
		SetTools([]ToolDef{{Name: "read", Description: "read files"}}).
		SetAgentPrompt("You are a test agent.").
		SetWebSearchWorkflow(3).
		SetPermissions(map[string]interface{}{"read": "allow"}).
		SetSubagentNames([]string{"explore"}).
		SetEnvInfo(&EnvInfo{Directory: "/out", Cwd: "/work", OS: "windows", Arch: "amd64"})

	out := b.Build()

	for _, want := range []string{
		"You are a test agent.",
		"## Environment",
		"## Available Tools",
		"- read: read files",
		"## Session Context",
		"- session_id: " + s.ID,
		"- agent: build",
		"## Tool Permissions",
		"- read: allow",
		"## Available Subagents",
		"- explore",
		"WORKFLOW: When you need to search for information:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestBuildWithoutOptionalSections(t *testing.T) {
	out := NewBuilder().SetAgentPrompt("hello").Build()
	if !strings.Contains(out, "hello") {
		t.Error("prompt body missing")
	}
	for _, unwanted := range []string{"## Environment", "## Session Context", "## Available Tools"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected section %q", unwanted)
		}
	}
}

func TestBuildEnvInfo(t *testing.T) {
	b := NewBuilder().
		SetAgentPrompt("p").
		SetEnvInfo(&EnvInfo{
			Directory: "out",
			OS:        "windows",
			Arch:      "amd64",
			EnvVars:   map[string]string{"PATH": "/bin"},
		})
	out := b.Build()
	for _, want := range []string{"- directory: out", "- os: windows", "## Shell Environment", "PATH=/bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}