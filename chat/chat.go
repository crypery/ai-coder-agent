package chat

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"ai-coder-agent/agent"
	"ai-coder-agent/agents"
	"ai-coder-agent/commands"
	"ai-coder-agent/logger"

	"github.com/google/uuid"
)

type Chat struct {
	agent  *agent.Agent
	log    *logger.Logger
	agents *agents.Manager
}

func New(a *agent.Agent, log *logger.Logger, agentsMgr *agents.Manager) *Chat {
	return &Chat{
		agent:  a,
		log:    log,
		agents: agentsMgr,
	}
}

func (c *Chat) Run() {
	id := uuid.New().String()
	c.log.LogStage(id, "Starting AI Agent Chat")
	c.log.Printf("Model: %s | API: %s", c.agent.GetModel(), c.agent.GetBaseURL())
	c.log.Printf("Output folder: %s", c.agent.GetOutputFolder())
	toolNames := make([]string, len(c.agent.ListTools()))
	for i, t := range c.agent.ListTools() {
		toolNames[i] = t.Name
	}
	c.log.Printf("Tools available: %s", strings.Join(toolNames, ", "))
	cmds := []string{"/quit", "/exit", "/help", "/clear", "/compact", "/tools", "/status", "/agents", "/switch"}
	c.log.Printf("Commands available: %s", strings.Join(cmds, ", "))
	c.log.Printf("Type your message and press Enter. Type 'quit' or 'exit' to leave.\n")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	c.log.Print("\n========== user ==========")
	for {
		fmt.Print("\n> ")
		input, ok := nextInput(scanner)
		if !ok {
			return
		}
		if input == "" {
			continue
		}

		parsed, isCmd := commands.Parse(input)
		if isCmd {
			switch parsed.Action {
			case commands.ActionQuit:
				id := uuid.New().String()
				c.log.LogStage(id, "User quit")
				return
			case commands.ActionHelp:
				c.log.Print(commands.HelpText)
				continue
			case commands.ActionClear:
				c.agent.ClearHistory()
				id := uuid.New().String()
				c.log.LogStage(id, "Chat history cleared")
				continue
			case commands.ActionCompact:
				if err := c.agent.CompactSession(); err != nil {
					id := uuid.New().String()
					c.log.LogError(id, err)
				} else {
					id := uuid.New().String()
					c.log.LogStage(id, "Session compacted")
				}
				continue
			case commands.ActionTools:
				c.printTools()
				continue
			case commands.ActionStatus:
				c.printStatus()
				continue
			case commands.ActionAgents:
				c.printAgents()
				continue
			case commands.ActionSwitch:
				c.log.Print("Available agents:", strings.Join(c.agents.List(), ", "))
				agentName := parsed.Arg
				if agentName == "" {
					fmt.Print("Enter agent name: ")
					if !scanner.Scan() {
						return
					}
					agentName = strings.TrimSpace(scanner.Text())
				}
				if _, err := c.agent.SwitchAgentCallback(agentName); err != nil {
					id := uuid.New().String()
					c.log.LogError(id, err)
				} else {
					c.log.Printf("Switched to agent: %s", agentName)
				}
				continue
			default:
				c.log.Printf("Unknown command: /%s. Type 'help' for available commands.", parsed.Name)
				continue
			}
		}

		if err := c.agent.ProcessMessage(input); err != nil {
			id := uuid.New().String()
			c.log.LogError(id, err)
		}
		c.log.Print("\n========== user ==========")
	}
}

// nextInput reads a single line from the scanner and returns it trimmed.
// Each press of Enter submits one message to the AI.
func nextInput(scanner *bufio.Scanner) (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	input := strings.TrimSpace(scanner.Text())
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	return input, true
}

func (c *Chat) printAgents() {
	c.log.Print("\n========== agents ==========\n")
	for _, name := range c.agents.List() {
		agent := c.agents.GetCurrent()
		if agent.Name == name {
			c.log.Printf("  - %s (current)", name)
		} else {
			c.log.Printf("  - %s", name)
		}
	}
	c.log.Print("")
}

func (c *Chat) printTools() {
	tools := c.agent.ListTools()
	c.log.Print("\n========== available tools ==========\n")
	for _, t := range tools {
		c.log.Printf("  - %s: %s", t.Name, t.Description)
	}
	c.log.Print("")
}

func (c *Chat) printStatus() {
	prompt, completion, total := c.agent.TokenUsage()
	c.log.Print("\n========== status ==========\n")
	c.log.Printf("  Model:              %s", c.agent.GetModel())
	c.log.Printf("  API Base:           %s", c.agent.GetBaseURL())
	c.log.Printf("  Output:             %s", c.agent.GetOutputFolder())
	c.log.Printf("  Messages:           %d", c.agent.MessageCount())
	c.log.Printf("  Tokens used:        %d", total)
	c.log.Printf("    prompt:           %d", prompt)
	c.log.Printf("    completion:       %d", completion)
	c.log.Print("")
}
