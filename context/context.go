package context

import (
	"fmt"
	"path/filepath"
	"strings"

	"ai-coder-agent/session"
)

// EnvInfo holds runtime environment details injected into the system prompt.
type EnvInfo struct {
	Directory string
	Cwd       string
	FileTree  string
	EnvVars   map[string]string
	OS        string
	Arch      string
}

// ToolDef represents a tool available to the agent.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]interface{}
}

// Builder assembles the full system prompt sent to the API.
type Builder struct {
	session      *session.Session
	tools        []ToolDef
	envInfo      *EnvInfo
	agentPrompt  string
	webLimit     int
	permissions  map[string]interface{}
	subagentNames []string
}

// NewBuilder creates a new Builder with sensible defaults.
func NewBuilder() *Builder {
	return &Builder{}
}

// SetSession configures the session context.
func (b *Builder) SetSession(s *session.Session) *Builder {
	b.session = s
	return b
}

// SetTools configures the available tools.
func (b *Builder) SetTools(tools []ToolDef) *Builder {
	b.tools = tools
	return b
}

// SetEnvInfo configures environment information.
func (b *Builder) SetEnvInfo(env *EnvInfo) *Builder {
	b.envInfo = env
	return b
}

// SetAgentPrompt sets the agent's base system prompt.
func (b *Builder) SetAgentPrompt(prompt string) *Builder {
	b.agentPrompt = prompt
	return b
}

// SetWebSearchWorkflow configures the web search workflow with the given limit.
func (b *Builder) SetWebSearchWorkflow(limit int) *Builder {
	b.webLimit = limit
	return b
}

// SetPermissions configures the agent's tool permissions.
func (b *Builder) SetPermissions(perms map[string]interface{}) *Builder {
	b.permissions = perms
	return b
}

// SetSubagentNames configures the list of available subagent names.
func (b *Builder) SetSubagentNames(names []string) *Builder {
	b.subagentNames = names
	return b
}

// Build returns the fully assembled system prompt.
func (b *Builder) Build() string {
	var sb strings.Builder

	// 1. Agent prompt (base)
	sb.WriteString(b.agentPrompt)

	// 2. Environment
	if b.envInfo != nil {
		sb.WriteString("\n\n## Environment\n")
		dir := b.envInfo.Directory
		if filepath.IsAbs(dir) && b.envInfo.Cwd != "" {
			if rel, err := filepath.Rel(b.envInfo.Cwd, dir); err == nil {
				dir = rel
			}
		}
		if dir == "" {
			dir = "."
		}
		sb.WriteString(fmt.Sprintf("- directory: %s\n", dir))
		sb.WriteString("(tool file paths are relative to this directory)\n")
		if b.envInfo.OS != "" {
			sb.WriteString(fmt.Sprintf("- os: %s\n", b.envInfo.OS))
		}
		if b.envInfo.Arch != "" {
			sb.WriteString(fmt.Sprintf("- arch: %s\n", b.envInfo.Arch))
		}
		if b.envInfo.FileTree != "" {
			sb.WriteString("\n## Files\n```\n" + b.envInfo.FileTree + "\n```\n")
		}
		if len(b.envInfo.EnvVars) > 0 {
			sb.WriteString("\n\n## Shell Environment\n")
			for key, val := range b.envInfo.EnvVars {
				sb.WriteString(fmt.Sprintf("%s=%s\n", key, val))
			}
		}
	}

	// 3. Tools
	if len(b.tools) > 0 {
		sb.WriteString("\n\n## Available Tools\n")
		for _, t := range b.tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}

	// 4. Session context
	if b.session != nil {
		sb.WriteString("\n\n## Session Context\n")
		sb.WriteString(fmt.Sprintf("- session_id: %s\n", b.session.ID))
		if b.session.ParentSession != "" {
			sb.WriteString(fmt.Sprintf("- parent_session_id: %s\n", b.session.ParentSession))
		}
		sb.WriteString(fmt.Sprintf("- agent: %s\n", b.session.AgentName))
		sb.WriteString(fmt.Sprintf("- mode: %s\n", b.session.Mode))
	}

	// 5. Permissions
	if len(b.permissions) > 0 {
		sb.WriteString("\n\n## Tool Permissions\n")
		for tool, perm := range b.permissions {
			sb.WriteString(fmt.Sprintf("- %s: %v\n", tool, perm))
		}
	}

	// 6. Available subagents
	if len(b.subagentNames) > 0 {
		sb.WriteString("\n\n## Available Subagents\n")
		sb.WriteString("Use switch_agent tool to delegate work to these subagents:\n")
		for _, name := range b.subagentNames {
			sb.WriteString("- " + name + "\n")
		}
	}

	// 7. Web search workflow
	if b.webLimit > 0 {
		sb.WriteString(fmt.Sprintf(`

WORKFLOW: When you need to search for information:
1. Call websearch ONCE with your query
2. From the results, select up to 3 URLs
3. Call webfetch for each selected URL to get the content (max %d times)
4. Analyze the content and provide the answer

Do NOT call websearch multiple times. Use webfetch to get content from the search results.`, b.webLimit))
	}

	return sb.String()
}
