// Package commands provides the shared chat command parser used by both the
// console and the web frontends so the two never drift apart.
package commands

import "strings"

// Action identifies a parsed chat command.
type Action int

const (
	// ActionMessage is a regular chat message (no leading "/").
	ActionMessage Action = iota
	ActionQuit
	ActionHelp
	ActionClear
	ActionCompact
	ActionTools
	ActionStatus
	ActionAgents
	ActionSwitch
	ActionUnknown
)

// Parsed is the result of parsing a chat input line.
type Parsed struct {
	Action Action
	Name   string
	Arg    string
}

// Parse classifies a chat input line. The second return value reports whether
// the input was a command (starts with "/").
func Parse(input string) (Parsed, bool) {
	if !strings.HasPrefix(input, "/") {
		return Parsed{Action: ActionMessage}, false
	}
	parts := strings.SplitN(input[1:], " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	switch cmd {
	case "quit", "exit":
		return Parsed{Action: ActionQuit, Name: cmd, Arg: arg}, true
	case "help":
		return Parsed{Action: ActionHelp, Name: cmd, Arg: arg}, true
	case "clear":
		return Parsed{Action: ActionClear, Name: cmd, Arg: arg}, true
	case "compact":
		return Parsed{Action: ActionCompact, Name: cmd, Arg: arg}, true
	case "tools":
		return Parsed{Action: ActionTools, Name: cmd, Arg: arg}, true
	case "status":
		return Parsed{Action: ActionStatus, Name: cmd, Arg: arg}, true
	case "agents":
		return Parsed{Action: ActionAgents, Name: cmd, Arg: arg}, true
	case "switch":
		return Parsed{Action: ActionSwitch, Name: cmd, Arg: arg}, true
	default:
		return Parsed{Action: ActionUnknown, Name: cmd, Arg: arg}, true
	}
}

// HelpText lists the available commands. Shown by both the console and the web
// frontend.
const HelpText = `Commands:
  <message>  - Send a message to the AI
  /quit      - Exit the program
  /exit      - Exit the program
  /clear     - Clear chat history
  /compact   - Compact the session
  /tools     - List available tools
  /status    - Show current status
  /agents    - List agents
  /switch    - Switch agent, e.g. /switch plan
  /help      - Show this help`