package agents

import (
	"fmt"
	"strings"
)

type Agent struct {
	Name           string
	Description    string
	Mode           string
	Model          string
	Temperature    float64
	WebSearchLimit int
	WebFetchLimit  int
	SystemPrompt   string
	Permissions    map[string]interface{}
	Disabled       bool
	Color          string
}

type Manager struct {
	agents         map[string]*Agent
	current        string
	onSwitched     []func(oldName, newName string)
}

func NewManager() *Manager {
	return &Manager{
		agents: make(map[string]*Agent),
	}
}

func (m *Manager) Register(agent *Agent) {
	m.agents[agent.Name] = agent
}

func (m *Manager) LoadBuiltIn() {
		m.Register(&Agent{
		Name:           "build",
		Description:    "Default agent with full access for development work",
		Mode:           "primary",
		Temperature:    0.2,
		WebSearchLimit: 1,
		WebFetchLimit:  3,
		SystemPrompt: `You are a helpful AI assistant with access to tools. You can write code, save files, execute shell commands, and read files.

Available tools:
- write: Save code or text to a file
- read: Read the contents of a file
- bash: Execute shell commands
- list: List files and directories
- edit: Edit files with exact string replacement
- grep: Search file contents using regex
- glob: Find files by pattern
- patch: Apply patches to files
- webfetch: Fetch content from URLs
- websearch: Search the web
- question: Ask user questions
- todowrite: Manage todo lists

WORKFLOW: When you need to search for information:
1. Call websearch ONCE with your query
2. From the results, select up to 3 URLs
3. Call webfetch for each selected URL to get the content (max 3 times)
4. Analyze the content and provide the answer

Do NOT call websearch multiple times. Use webfetch to get content from the search results.`,
		Permissions: map[string]interface{}{
			"read":  "allow",
			"edit":  "allow",
			"bash":  "allow",
		},
	})

		m.Register(&Agent{
		Name:           "plan",
		Description:    "Restricted agent for planning and analysis without making changes",
		Mode:           "primary",
		Temperature:    0.1,
		WebSearchLimit: 1,
		WebFetchLimit:  1,
		SystemPrompt: `You are a planning agent. Analyze code, suggest changes, or create plans without making actual modifications to the codebase.

Focus on:
- Code analysis and review
- Architecture suggestions
- Implementation plans
- Potential issues and edge cases

Do NOT modify files unless explicitly requested. Provide detailed explanations and recommendations.`,
		Permissions: map[string]interface{}{
			"read":  "allow",
			"edit":  "deny",
			"bash":  "deny",
		},
	})

	m.Register(&Agent{
		Name:           "general",
		Description:    "General-purpose agent for researching complex questions and executing multi-step tasks",
		Mode:           "subagent",
		Temperature:    0.2,
		WebSearchLimit: 1,
		WebFetchLimit:  3,
		SystemPrompt: `You are a general-purpose agent for researching complex questions and executing multi-step tasks. You have full tool access (except todo), so you can make file changes when needed.

Use this agent to:
- Research complex questions
- Execute multi-step tasks
- Run multiple units of work in parallel
- Make file changes when needed

Be thorough and methodical in your approach. Provide clear explanations of your findings and actions.`,
		Permissions: map[string]interface{}{
			"read":      "allow",
			"edit":      "allow",
			"bash":      "allow",
			"todowrite": "deny",
		},
	})

	m.Register(&Agent{
		Name:           "explore",
		Description:    "Fast, read-only agent for exploring codebases",
		Mode:           "subagent",
		Temperature:    0.1,
		WebSearchLimit: 0,
		WebFetchLimit:  0,
		SystemPrompt: `You are a fast, read-only agent for exploring codebases. You cannot modify files.

Use this agent to:
- Quickly find files by patterns
- Search code for keywords
- Answer questions about the codebase
- Explore directory structures

Be efficient and focused. Provide concise results for the exploration task.`,
		Permissions: map[string]interface{}{
			"read":  "allow",
			"edit":  "deny",
			"bash":  "deny",
		},
	})

		m.Register(&Agent{
		Name:           "scout",
		Description:    "Read-only agent for external docs and dependency research",
		Mode:           "subagent",
		Temperature:    0.1,
		WebSearchLimit: 1,
		WebFetchLimit:  1,
		SystemPrompt: `You are a read-only agent for external docs and dependency research.

Use this agent to:
- Inspect library source code
- Cross-reference local code against upstream implementations
- Research external documentation
- Analyze dependencies without modifying the workspace

Be thorough in your research. Provide detailed findings about the external dependencies and documentation.`,
		Permissions: map[string]interface{}{
			"read":  "allow",
			"edit":  "deny",
			"bash":  "deny",
		},
	})

	m.Register(&Agent{
		Name:           "code-reviewer",
		Description:    "Reviews code for best practices and potential issues",
		Mode:           "subagent",
		Temperature:    0.1,
		WebSearchLimit: 0,
		WebFetchLimit:  0,
		SystemPrompt: `You are a code reviewer. Focus on security, performance, and maintainability.

Use this agent to:
- Review code quality and best practices
- Identify potential bugs and edge cases
- Check performance implications
- Verify security considerations

Provide constructive feedback without making direct changes.`,
		Permissions: map[string]interface{}{
			"read":  "allow",
			"edit":  "deny",
			"bash":  "deny",
		},
	})
}

func parseMarkdownAgent(content, name string) *Agent {
	lines := strings.Split(content, "\n")
	var frontmatter, prompt string
	inFrontmatter := false

	for _, line := range lines {
		if line == "---" {
			if inFrontmatter {
				inFrontmatter = false
			} else {
				inFrontmatter = true
			}
			continue
		}
		if inFrontmatter {
			frontmatter += line + "\n"
		} else {
			prompt += line + "\n"
		}
	}

	agent := &Agent{
		Name: name,
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "description:") {
			agent.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "mode:") {
			agent.Mode = strings.TrimSpace(strings.TrimPrefix(line, "mode:"))
		} else if strings.HasPrefix(line, "model:") {
			agent.Model = strings.TrimSpace(strings.TrimPrefix(line, "model:"))
		} else if strings.HasPrefix(line, "temperature:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "temperature:")), "%f", &agent.Temperature)
		} else if strings.HasPrefix(line, "websearch_limit:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "websearch_limit:")), "%d", &agent.WebSearchLimit)
		} else if strings.HasPrefix(line, "webfetch_limit:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "webfetch_limit:")), "%d", &agent.WebFetchLimit)
		} else if strings.HasPrefix(line, "disable:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "disable:"))
			agent.Disabled = val == "true" || val == "yes"
		}
	}

	permissionLines := strings.Split(frontmatter, "\n")
	inPermission := false
	for _, line := range permissionLines {
		if strings.TrimSpace(line) == "permission:" {
			inPermission = true
			continue
		}
		if inPermission {
			// Indentation is what separates permission entries from the rest of
			// the frontmatter, so it must be checked before trimming.
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
				if len(parts) == 2 {
					if agent.Permissions == nil {
						agent.Permissions = make(map[string]interface{})
					}
					agent.Permissions[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
			} else if strings.TrimSpace(line) == "" {
				continue
			} else {
				inPermission = false
			}
		}
	}

	agent.SystemPrompt = strings.TrimSpace(prompt)
	return agent
}

func (m *Manager) SetCurrent(name string) error {
	if _, exists := m.agents[name]; !exists {
		return fmt.Errorf("agent not found: %s", name)
	}
	oldName := m.current
	if oldName == name {
		return nil
	}
	m.current = name
	for _, cb := range m.onSwitched {
		cb(oldName, name)
	}
	return nil
}

func (m *Manager) OnSwitched(cb func(oldName, newName string)) {
	m.onSwitched = append(m.onSwitched, cb)
}

func (m *Manager) GetSubagentNames() []string {
	var names []string
	for _, a := range m.agents {
		if a.Mode == "subagent" && !a.Disabled {
			names = append(names, a.Name)
		}
	}
	return names
}

func (m *Manager) GetCurrent() *Agent {
	if m.current == "" {
		return m.GetDefault()
	}
	return m.agents[m.current]
}

func (m *Manager) GetDefault() *Agent {
	if agent, exists := m.agents["build"]; exists {
		return agent
	}
	if agent, exists := m.agents["general"]; exists {
		return agent
	}
	for _, agent := range m.agents {
		if agent.Mode == "primary" && !agent.Disabled {
			return agent
		}
	}
	return nil
}

func (m *Manager) List() []string {
	var names []string
	for name, agent := range m.agents {
		if !agent.Disabled {
			names = append(names, name)
		}
	}
	return names
}

func (m *Manager) GetAgentConfig() *Agent {
	agent := m.GetCurrent()
	if agent.Disabled {
		agent = m.GetDefault()
	}
	return agent
}
