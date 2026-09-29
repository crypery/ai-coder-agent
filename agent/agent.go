package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-coder-agent/agents"
	"ai-coder-agent/api"
	"ai-coder-agent/config"
	"ai-coder-agent/context"
	"ai-coder-agent/env"
	"ai-coder-agent/logger"
	"ai-coder-agent/session"
	"ai-coder-agent/tools"

	"github.com/google/uuid"
)

type Agent struct {
	cfg                   *config.Config
	api                   *api.Client
	tools                 *tools.Registry
	log                   *logger.Logger
	agentsMgr             *agents.Manager
	messages              []api.Message
	systemPrompt          string
	totalPromptTokens     int
	totalCompletionTokens int
	totalTokens           int
	websearchCount        int
	webfetchCount         int
	compactThreshold      int
	envInfo               *env.Info
	session               *session.Session
	stopRequest           bool
	stopMu                sync.Mutex
	parentMessages        []api.Message
	subagentName          string
	// stateMu guards the message transcript and token counters which are read
	// concurrently by the web UI stats while a request is being processed.
	stateMu sync.RWMutex
}

func New(cfg *config.Config, log *logger.Logger, agentsMgr *agents.Manager) (*Agent, error) {
	executor := &tools.ShellExecutor{Timeout: cfg.BashTimeout, Dir: cfg.OutputFolder}
	registry := tools.NewRegistry(cfg.OutputFolder, executor, nil, nil, nil, cfg.WebFetchLimit, cfg.CompressWebText, cfg.WebClientTimeout)

	a := &Agent{
		cfg:              cfg,
		api:              api.New(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.APITimeout),
		tools:            registry,
		log:              log,
		agentsMgr:        agentsMgr,
		compactThreshold: int(float64(cfg.MaxTokens) * 0.95),
	}

	a.tools.SetCallbacks(a.pathPermissionCallback, a.questionCallback, a.SwitchAgentCallback)

	// Read agent config for the base prompt and web limits.
	agentConfig := agentsMgr.GetAgentConfig()

	// Pass agent permissions to the tools registry.
	a.tools.SetAgentPermissions(agentConfig.Permissions)

	// Create session with agent metadata.
	a.session = session.New(agentConfig.Name, agentConfig.Mode)

	// Collect environment information for the system prompt.
	if a.cfg.IncludeEnvInfo {
		maxFiles := a.cfg.MaxFileTree
		if maxFiles <= 0 {
			maxFiles = 100
		}
		a.envInfo = env.Collect(a.cfg.OutputFolder, maxFiles)

		// Collect environment variables if keys are specified.
		if len(a.cfg.EnvVarKeys) > 0 {
			a.envInfo.EnvVars = env.CollectEnvVars(a.cfg.EnvVarKeys)
		}
	}

	a.systemPrompt = a.buildSystemPrompt(agentConfig)

	a.appendMessage(api.Message{
		Role:    "system",
		Content: a.systemPrompt,
	})

	return a, nil
}

// appendMessage appends a message to the transcript under the state lock.
func (a *Agent) appendMessage(msg api.Message) {
	a.stateMu.Lock()
	a.messages = append(a.messages, msg)
	a.stateMu.Unlock()
}

// setMessages replaces the entire transcript under the state lock.
func (a *Agent) setMessages(msgs []api.Message) {
	a.stateMu.Lock()
	a.messages = msgs
	a.stateMu.Unlock()
}

func (a *Agent) ProcessMessage(userInput string) error {
	stageID := uuid.New().String()
	a.log.LogStage(stageID, "receiving user input")

	// Rebuild system prompt before each request (includes updated session context).
	agentConfig := a.agentsMgr.GetAgentConfig()
	a.stateMu.Lock()
	a.messages[0] = api.Message{
		Role:    "system",
		Content: a.buildSystemPrompt(agentConfig),
	}
	a.stateMu.Unlock()

	a.websearchCount = 0
	a.webfetchCount = 0
	a.stopMu.Lock()
	a.stopRequest = false
	a.stopMu.Unlock()

	a.appendMessage(api.Message{
		Role:    "user",
		Content: userInput,
	})

	a.log.LogRequest(stageID, a.messages[0].Content.(string), userInput)

	start := time.Now()
	totalAPITime := time.Duration(0)
	err := a.runLoop(&totalAPITime)
	totalDuration := time.Since(start)

	if a.totalTokens > 0 {
		pct := float64(a.totalTokens) / float64(a.cfg.MaxTokens) * 100
		a.log.PrintfActivity("\ntokens: %d / %d (%.1f%%) | time: %s", a.totalTokens, a.cfg.MaxTokens, pct, totalDuration.Round(100*time.Millisecond))
	}

	return err
}

func (a *Agent) runLoop(totalAPITime *time.Duration) error {
	toolDefs := a.convertToolDefs(a.tools.GetAll())

	for {
		stageID := uuid.New().String()
		a.log.LogStage(stageID, "API call")

		// Refresh tool definitions and limits in case agent was switched
		currentAgent := a.agentsMgr.GetAgentConfig()
		if currentAgent.WebSearchLimit != a.cfg.WebSearchLimit || currentAgent.WebFetchLimit != a.cfg.WebFetchLimit {
			a.cfg.WebSearchLimit = currentAgent.WebSearchLimit
			a.cfg.WebFetchLimit = currentAgent.WebFetchLimit
			toolDefs = a.convertToolDefs(a.tools.GetAll())
		}

		requestMessages := a.messages
		if !containsUserQuery(requestMessages) {
			// Multi-step tool chat templates (e.g. Qwen3.5) reject requests
			// that contain no user query at all. This can happen when session
			// compaction replaces the conversation with a summary while a tool
			// loop is still running.
			requestMessages = append(append([]api.Message{}, a.messages...), api.Message{
				Role:    "user",
				Content: "continue with the current task based on the conversation summary, provide the final answer or call tools if more information is needed.",
			})
		}

		apiStart := time.Now()
		agentID := uuid.New().String()
		a.log.LogAgentCall(agentID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, len(requestMessages), len(toolDefs))

		resp, err := a.api.SendMessage(requestMessages, toolDefs, a.cfg.MaxTokens, a.cfg.Temperature)
		apiDuration := time.Since(apiStart)
		*totalAPITime += apiDuration
		if err != nil {
			id := uuid.New().String()
			a.log.LogError(id, fmt.Errorf("api call: %w", err))
			return fmt.Errorf("api call: %w", err)
		}

		respStageID := uuid.New().String()
		a.log.LogStage(respStageID, "received response from API")

		choice := resp.Choices[0]
		msg := choice.Message

		a.stateMu.Lock()
		a.totalPromptTokens = resp.Usage.PromptTokens
		a.totalCompletionTokens = resp.Usage.CompletionTokens
		a.totalTokens = resp.Usage.TotalTokens
		a.stateMu.Unlock()

		a.log.LogAgentResult(agentID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, resp.Model, len(resp.Choices), resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, choice.FinishReason)

		if a.checkCompact() {
			compactStageID := uuid.New().String()
			a.log.LogStage(compactStageID, "token threshold reached, compacting session")
			if err := a.CompactSession(); err != nil {
				id := uuid.New().String()
				a.log.LogError(id, fmt.Errorf("compact session: %w", err))
			}
			a.appendMessage(api.Message{
				Role:    "user",
				Content: "session was auto-compacted to preserve context, continue with the current task.",
			})
		}

		if msg.Content != "" && len(msg.ToolCalls) == 0 {
			content, ok := msg.Content.(string)
			if !ok {
				content = fmt.Sprintf("%v", msg.Content)
			}
			a.appendMessage(api.Message{
				Role:    "assistant",
				Content: content,
			})
			respID := uuid.New().String()
			a.log.LogResponse(respID, content)

			// Restore parent agent context if we were acting as a subagent.
			if a.session.Mode == "subagent" && len(a.parentMessages) > 0 {
				restoreStageID := uuid.New().String()
				a.log.LogStage(restoreStageID, "restoring parent agent context")

				parentAgent := a.agentsMgr.GetDefault()
				subagentName := a.subagentName
				if subagentName == "" {
					subagentName = a.session.AgentName
				}
				a.subagentName = ""
				a.session.AgentName = parentAgent.Name
				a.session.Mode = parentAgent.Mode
				a.systemPrompt = a.buildSystemPrompt(parentAgent)

				// Replace the assistant message with a labeled subagent result.
				a.setMessages(append(a.parentMessages, api.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("Subagent '%s' returned: %s", subagentName, content),
				}))
				a.parentMessages = nil
				a.tools.SetAgentPermissions(parentAgent.Permissions)
			}

			return nil
		}

		if len(msg.ToolCalls) > 0 {
			a.appendMessage(api.Message{
				Role:      "assistant",
				Content:   nil,
				ToolCalls: msg.ToolCalls,
			})

			for _, tc := range msg.ToolCalls {
				if err := a.handleToolCall(tc); err != nil {
					id := uuid.New().String()
					a.log.LogError(id, fmt.Errorf("tool call %s: %w", tc.Function.Name, err))
				}
				a.stopMu.Lock()
				stopped := a.stopRequest
				a.stopMu.Unlock()
				if stopped {
					break
				}
			}

			a.stopMu.Lock()
			stopped := a.stopRequest
			a.stopMu.Unlock()
			if stopped {
				// The user denied a permission prompt, so the remaining work is
				// cancelled: announce the stop and leave the loop.
				stopText := "Request stopped: permission denied by the user."
				a.appendMessage(api.Message{Role: "assistant", Content: stopText})
				respID := uuid.New().String()
				a.log.LogResponse(respID, stopText)
				stopStageID := uuid.New().String()
				a.log.LogStage(stopStageID, "Request stopped (permission denied)")
				return nil
			}

			roundStageID := uuid.New().String()
			a.log.LogStage(roundStageID, "round complete, checking for more tool calls...")
		} else {
			emptyStageID := uuid.New().String()
			a.log.LogStage(emptyStageID, "no content or tool calls in response")
			a.log.LogDone(emptyStageID)
			return nil
		}
	}
}

// containsUserQuery reports whether the messages contain a genuine user turn:
// non-empty content that is not only a wrapper around a tool response. Some
// multi-step tool chat templates (e.g. Qwen3.5) reject requests with no user
// query, so the caller must make sure one is present before sending.
func containsUserQuery(messages []api.Message) bool {
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		content, ok := msg.Content.(string)
		if !ok {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if strings.HasPrefix(content, "<tool_response>") && strings.HasSuffix(content, "</tool_response>") {
			continue
		}
		return true
	}
	return false
}

func (a *Agent) handleToolCall(tc api.ToolCall) error {

	if strings.ToLower(tc.Function.Name) == "websearch" {
		websearchLimit := a.cfg.WebSearchLimit
		if websearchLimit > 0 && a.websearchCount >= websearchLimit {
			a.appendMessage(api.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("websearch limit reached (%d per request). You can only use websearch once per user request.", websearchLimit),
				ToolCallID: tc.ID,
			})
			return nil
		}
		a.websearchCount++
	}

	if strings.ToLower(tc.Function.Name) == "webfetch" {
		webfetchLimit := a.cfg.WebFetchLimit
		if webfetchLimit > 0 && a.webfetchCount >= webfetchLimit {
			a.appendMessage(api.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("webfetch limit reached (%d per request). You can only use webfetch %d times per user request.", webfetchLimit, webfetchLimit),
				ToolCallID: tc.ID,
			})
			return nil
		}
		a.webfetchCount++
	}

	logStr, args:= a.tools.ChangePoint(tc.Function.Name, tc.Function.Arguments)

	toolEventID := uuid.New().String()
	a.log.LogToolCall(toolEventID, tc.Function.Name, logStr)

	result, err := a.tools.Execute(tc.Function.Name, args)
	if err != nil {
		id := uuid.New().String()
		a.log.LogError(id, fmt.Errorf("execute tool: %w", err))
		result = &tools.ToolResult{
			Content: fmt.Sprintf("error: %v", err),
			Success: false,
		}
	}


	a.log.LogToolResult(toolEventID, tc.Function.Name, result.Content)

	if result.Denied {
		a.stopMu.Lock()
		a.stopRequest = true
		a.stopMu.Unlock()
	}

	a.appendMessage(api.Message{
		Role:       "tool",
		Content:    result.Content,
		ToolCallID: tc.ID,
	})

	return nil
}

func (a *Agent) convertToolDefs(tools []tools.Tool) []api.ToolDef {
	result := make([]api.ToolDef, len(tools))
	for i, t := range tools {
		result[i] = api.ToolDef{
			Type: "function",
			Function: map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Schema,
			},
		}
	}
	return result
}

func (a *Agent) GetToolDefinitions() []tools.Tool {
	return a.tools.GetAll()
}

// SetCallbacks replaces the interactive callbacks used by tools that need to
// ask the user for input (permission prompts and the "question" tool). The
// console defaults read from stdin; the web server ovides them to route the
// prompt through the websocket.
func (a *Agent) SetCallbacks(onPathAsk tools.PathPermissionCallback, onQuestionAsk tools.QuestionCallback, onSwitchAgent tools.SwitchAgentCallback) {
	a.tools.SetCallbacks(onPathAsk, onQuestionAsk, onSwitchAgent)
}

func (a *Agent) GetModel() string {
	return a.cfg.Model
}

func (a *Agent) GetBaseURL() string {
	return a.cfg.BaseURL
}

func (a *Agent) GetOutputFolder() string {
	absPath, _ := filepath.Abs(a.cfg.OutputFolder)
	return absPath
}

func (a *Agent) ListTools() []tools.Tool {
	return a.tools.GetAll()
}

func (a *Agent) HandleWebPermission(kind, target string, result tools.PermissionResult) {
	a.tools.HandleWebPermission(kind, target, result)
}

func (a *Agent) Stop() {
	a.stopMu.Lock()
	a.stopRequest = true
	a.stopMu.Unlock()
}

func (a *Agent) MessageCount() int {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return len(a.messages)
}

func (a *Agent) TokenUsage() (prompt, completion, total int) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.totalPromptTokens, a.totalCompletionTokens, a.totalTokens
}

func (a *Agent) ClearHistory() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if len(a.messages) > 0 {
		systemMsg := a.messages[0]
		a.messages = []api.Message{systemMsg}
	}
}

func (a *Agent) checkCompact() bool {
	if !a.cfg.AutoCompact {
		return false
	}
	if a.totalTokens < a.compactThreshold {
		return false
	}
	return true
}

func (a *Agent) CompactSession() error {
	stageID := uuid.New().String()
	a.log.LogStage(stageID, "compacting session")

	var textMessages []api.Message
	for _, msg := range a.messages {
		if msg.Role == "system" {
			continue
		}
		if msg.Content != "" {
			if content, ok := msg.Content.(string); ok {
				textMessages = append(textMessages, api.Message{
					Role:    msg.Role,
					Content: content,
				})
			}
		}
	}

	compactPrompt := api.Message{
		Role:    "user",
		Content: fmt.Sprintf("summarize this entire conversation in detail"),
	}

	allMessages := append(textMessages, compactPrompt)

	agentID := uuid.New().String()
	a.log.LogAgentCall(agentID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, len(allMessages), 0)
	resp, err := a.api.SendMessage(allMessages, nil, a.cfg.MaxTokens, a.cfg.Temperature)
	if err == nil {
		a.log.LogAgentResult(agentID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, resp.Model, len(resp.Choices), resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, resp.Choices[0].FinishReason)
	}
	if err != nil {
		id := uuid.New().String()
		a.log.LogError(id, fmt.Errorf("compact session API call: %w", err))
		return fmt.Errorf("compact session API call: %w", err)
	}

	summary := ""
	if len(resp.Choices) > 0 && resp.Choices[0].Message.Content != "" {
		if content, ok := resp.Choices[0].Message.Content.(string); ok {
			summary = content
		}
	}

	if summary == "" {
		return fmt.Errorf("compact session returned empty summary")
	}

	// Create a new session with parent reference after compaction.
	a.session = a.session.WithParent(a.session.ID)

	// Rebuild system prompt with summary.
	a.setMessages([]api.Message{
		{
			Role:    "system",
			Content: a.buildSystemPrompt(a.agentsMgr.GetAgentConfig()) + "\n\n## Conversation Summary\n\n" + summary,
		},
	})

	compactDoneID := uuid.New().String()
	a.log.LogStage(compactDoneID, "Session compacted successfully")
	a.log.Printf("session compacted, new token count: %d", a.totalTokens)

	return nil
}

func (a *Agent) VerifyConnection() error {
	testMsg := api.Message{
		Role:    "user",
		Content: "Hello",
	}
	agentEventID := uuid.New().String()
	a.log.LogAgentCall(agentEventID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, 1, 0)
	resp, err := a.api.SendMessage([]api.Message{testMsg}, nil, 100, 0.5)
	if err == nil {
		a.log.LogAgentResult(agentEventID, a.agentsMgr.GetCurrent().Name, a.cfg.Model, resp.Model, len(resp.Choices), resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, resp.Choices[0].FinishReason)
	}
	return err
}

func (a *Agent) pathPermissionCallback(path string) (tools.PermissionResult, error) {
	fmt.Printf("\nPath outside output folder detected: %s\nAllow? [y]es / [n]o / [a]ll: ", path)

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return tools.PermissionDeny, err
	}
	response = strings.ToLower(strings.TrimSpace(response))

	switch response {
	case "y", "yes":
		return tools.PermissionAllow, nil
	case "a", "all":
		return tools.PermissionAllowAll, nil
	case "n", "no", "":
		return tools.PermissionDeny, nil
	default:
		return tools.PermissionDeny, nil
	}
}

func (a *Agent) questionCallback(question string, options []string) (string, error) {
	fmt.Printf("\n========== question ==========\n%s\n", question)
	if len(options) > 0 {
		for i, opt := range options {
			fmt.Printf("  %d. %s\n", i+1, opt)
		}
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Answer: ")
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(response), nil
}

func (a *Agent) SwitchAgentCallback(agentName string) (string, error) {
	oldAgent := a.agentsMgr.GetCurrent()
	if err := a.agentsMgr.SetCurrent(agentName); err != nil {
		return fmt.Sprintf("switch error: %v", err), err
	}

	agentConfig := a.agentsMgr.GetAgentConfig()

	// Update session metadata on agent switch.
	a.session.AgentName = agentConfig.Name
	a.session.Mode = agentConfig.Mode

	// Save parent context when switching to a subagent.
	if agentConfig.Mode == "subagent" {
		a.subagentName = agentConfig.Name
		a.parentMessages = make([]api.Message, len(a.messages))
		copy(a.parentMessages, a.messages)
	}

	// Update agent permissions in the tools registry.
	a.tools.SetAgentPermissions(agentConfig.Permissions)

	a.systemPrompt = a.buildSystemPrompt(agentConfig)
	a.setMessages(append([]api.Message{{Role: "system", Content: a.systemPrompt}}, a.messages[1:]...))

	a.websearchCount = 0
	a.webfetchCount = 0

	stageID := uuid.New().String()
	a.log.LogStage(stageID, "agent switched: "+agentName)
	respID := uuid.New().String()
	a.log.LogResponse(respID, fmt.Sprintf("switched from '%s' to '%s'.", oldAgent.Name, agentName))

	return fmt.Sprintf("successfully switched to agent: %s.", agentName), nil
}

// buildSystemPrompt constructs the full system prompt for the given agent
// configuration using the context.Builder. It includes environment info,
// available tools, session context, and web search workflow.
func (a *Agent) buildSystemPrompt(agentConfig *agents.Agent) string {
	toolDefs := a.convertContextToolDefs(a.tools.GetAll())

	b := context.NewBuilder()
	b.SetSession(a.session)
	b.SetTools(toolDefs)
	b.SetEnvInfo(&context.EnvInfo{
		Directory: a.cfg.OutputFolder,
		Cwd:       a.envInfo.Cwd,
		FileTree:  a.envInfo.FileTree,
		OS:        a.envInfo.OS,
		Arch:      a.envInfo.Arch,
	})
	b.SetAgentPrompt(agentConfig.SystemPrompt)
	b.SetWebSearchWorkflow(agentConfig.WebSearchLimit)
	b.SetPermissions(agentConfig.Permissions)
	b.SetSubagentNames(a.agentsMgr.GetSubagentNames())

	return b.Build()
}

func (a *Agent) convertContextToolDefs(tools []tools.Tool) []context.ToolDef {
	result := make([]context.ToolDef, len(tools))
	for i, t := range tools {
		result[i] = context.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
		}
	}
	return result
}
