package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Tool struct {
	Name        string
	Description string
	Schema      map[string]interface{}
}

type ToolResult struct {
	Content string
	Success bool
	Error   string
	// Denied marks a result that was rejected by the user (app/path
	// permission). The agent stops the current request if any tool is denied.
	Denied bool
}

type Registry struct {
	tools           map[string]Tool
	executor        Executor
	baseDir         string
	permDir         string
	allowedPaths    map[string]bool
	deniedPaths     map[string]bool
	onPathAsk       PathPermissionCallback
	onQuestionAsk   QuestionCallback
	onSwitchAgent   SwitchAgentCallback
	webfetchLimit   int
	compressWebText bool
	agentPerms      map[string]interface{}
	// webClient performs webfetch/websearch HTTP requests with a configurable
	// timeout (TLS verification is skipped to support local AI backends).
	webClient *http.Client
	// permMu guards the permission maps (allowedPaths, deniedPaths) which are
	// read and written concurrently by tool execution (path checks) and the
	// web permission handler.
	permMu sync.Mutex
}

type PathPermissionCallback func(path string) (PermissionResult, error)
type QuestionCallback func(question string, options []string) (string, error)
type SwitchAgentCallback func(agentName string) (string, error)

type PermissionResult int

const (
	PermissionAllow PermissionResult = iota
	PermissionDeny
	PermissionAllowAll
	PermissionDenyAll
)

type Executor interface {
	Run(command string) (*ToolResult, error)
}

type ShellExecutor struct {
	Timeout int
	Dir     string
}

// fakeAppPrefixes lists destructive app name prefixes. Commands that launch
// such apps (shutdown*, rm*, del*) are never executed for real: the bash tool
// returns a simulated success instead.
var fakeAppPrefixes = []string{"shutdown", "rm", "del"}

const permissionFile = "permission.txt"

func NewRegistry(baseDir string, executor Executor, onPathAsk PathPermissionCallback, onQuestionAsk QuestionCallback, onSwitchAgent SwitchAgentCallback, webfetchLimit int, compressWebText bool, webTimeoutSeconds int) *Registry {
	permDir, _ := os.Getwd()

	if webTimeoutSeconds < 0 {
		webTimeoutSeconds = 15
	}

	r := &Registry{
		tools:           make(map[string]Tool),
		executor:        executor,
		baseDir:         baseDir,
		permDir:         permDir,
		allowedPaths:    make(map[string]bool),
		deniedPaths:     make(map[string]bool),
		onPathAsk:       onPathAsk,
		onQuestionAsk:   onQuestionAsk,
		onSwitchAgent:   onSwitchAgent,
		webfetchLimit:   webfetchLimit,
		compressWebText: compressWebText,
		webClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: time.Duration(webTimeoutSeconds) * time.Second,
		},
	}

	// Load persistent permissions
	r.loadPersistentPermissions()

	r.registerTools()
	return r
}

func (r *Registry) loadPersistentPermissions() {
	fullPath := filepath.Join(r.permDir, permissionFile)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		kind := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if kind == "" || value == "" {
			continue
		}
		switch kind {
		case "allowed_paths":
			r.allowedPaths[normalizePath(value)] = true
		case "denied_paths":
			r.deniedPaths[normalizePath(value)] = true
		}
	}
}

func (r *Registry) savePersistentPermissions(baseDir string) {
	r.permMu.Lock()
	defer r.permMu.Unlock()
	var lines []string
	for k := range r.allowedPaths {
		lines = append(lines, "allowed_paths|"+k)
	}
	for k := range r.deniedPaths {
		lines = append(lines, "denied_paths|"+k)
	}
	fullPath := filepath.Join(baseDir, permissionFile)
	os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")), 0644)
}

func (r *Registry) HandleWebPermission(kind, target string, result PermissionResult) {
	normalized := normalizePath(target)
	r.permMu.Lock()
	switch kind {
	case "path":
		switch result {
		case PermissionAllowAll:
			r.allowedPaths[normalized] = true
		case PermissionDenyAll:
			r.deniedPaths[normalized] = true
		}
	}
	r.permMu.Unlock()
	r.savePersistentPermissions(r.permDir)
}

func (r *Registry) registerTools() {
	r.tools["write"] = Tool{
		Name:        "write",
		Description: "Create a new file or overwrite an existing file with the given content.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]string{
					"type":        "string",
					"description": "Path to the file to write",
				},
				"content": map[string]string{
					"type":        "string",
					"description": "Content to write to the file",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}

	r.tools["read"] = Tool{
		Name:        "read",
		Description: "Read the contents of a file.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]string{
					"type":        "string",
					"description": "Path to the file to read",
				},
			},
			"required": []string{"file_path"},
		},
	}

	r.tools["bash"] = Tool{
		Name:        "bash",
		Description: "Execute a shell command. Returns the output and exit code.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]string{
					"type":        "string",
					"description": "The shell command to execute",
				},
			},
			"required": []string{"command"},
		},
	}

	r.tools["list"] = Tool{
		Name:        "list",
		Description: "List files and directories in a path.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]string{
					"type":        "string",
					"description": "Directory path to list",
				},
			},
			"required": []string{"path"},
		},
	}

	r.tools["edit"] = Tool{
		Name:        "edit",
		Description: "Modify an existing file by replacing exact text. Old string must match exactly.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]string{
					"type":        "string",
					"description": "Path to the file to edit",
				},
				"old_string": map[string]string{
					"type":        "string",
					"description": "Exact text to find and replace",
				},
				"new_string": map[string]string{
					"type":        "string",
					"description": "Text to replace with",
				},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}

	r.tools["grep"] = Tool{
		Name:        "grep",
		Description: "Search file contents using regular expressions. Returns matching lines with file paths.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]string{
					"type":        "string",
					"description": "Regex pattern to search for",
				},
				"path": map[string]string{
					"type":        "string",
					"description": "Path to search in (file or directory)",
				},
			},
			"required": []string{"pattern", "path"},
		},
	}

	r.tools["glob"] = Tool{
		Name:        "glob",
		Description: "Find files matching a glob pattern (e.g., **/*.go, src/**/*.ts).",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]string{
					"type":        "string",
					"description": "Glob pattern to match",
				},
			},
			"required": []string{"pattern"},
		},
	}

	r.tools["patch"] = Tool{
		Name:        "patch",
		Description: "Apply a patch/diff to a file. Supports unified diff format.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]string{
					"type":        "string",
					"description": "Path to the file to patch",
				},
				"patch": map[string]string{
					"type":        "string",
					"description": "Patch content in unified diff format",
				},
			},
			"required": []string{"file_path", "patch"},
		},
	}

	r.tools["webfetch"] = Tool{
		Name:        "webfetch",
		Description: "Fetch content from a URL. Returns the page content as text.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]string{
					"type":        "string",
					"description": "URL to fetch",
				},
			},
			"required": []string{"url"},
		},
	}

	r.tools["websearch"] = Tool{
		Name:        "websearch",
		Description: "Search the web for information. Returns search results.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]string{
					"type":        "string",
					"description": "Search query",
				},
			},
			"required": []string{"query"},
		},
	}

	r.tools["question"] = Tool{
		Name:        "question",
		Description: "Ask the user a question during execution. Returns user's answer.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"question": map[string]string{
					"type":        "string",
					"description": "The question to ask",
				},
				"options": map[string]string{
					"type":        "string",
					"description": "Comma-separated list of options",
				},
			},
			"required": []string{"question"},
		},
	}

	r.tools["todowrite"] = Tool{
		Name:        "todowrite",
		Description: "Create or update a todo list to track progress during complex operations.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"todos": map[string]string{
					"type":        "string",
					"description": "JSON array of todo items with content, status, and priority",
				},
			},
			"required": []string{"todos"},
		},
	}

	r.tools["switch_agent"] = Tool{
		Name:        "switch_agent",
		Description: "Switch to a different agent (explore, scout, general) to delegate work. The agent changes its system prompt, permissions, and tool limits. Use when a specialized agent would be more efficient for the task.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent_name": map[string]string{
					"type":        "string",
					"description": "Name of the agent to switch to (explore, scout, general)",
				},
			},
			"required": []string{"agent_name"},
		},
	}
}

func (r *Registry) SetCallbacks(onPathAsk PathPermissionCallback, onQuestionAsk QuestionCallback, onSwitchAgent SwitchAgentCallback) {
	r.onPathAsk = onPathAsk
	r.onQuestionAsk = onQuestionAsk
	r.onSwitchAgent = onSwitchAgent
}

func (r *Registry) SetAgentPermissions(perms map[string]interface{}) {
	r.agentPerms = perms
}

func (r *Registry) checkAgentPermission(toolName string) *ToolResult {
	if r.agentPerms == nil {
		return nil
	}
	perm, ok := r.agentPerms[toolName]
	if !ok {
		return nil
	}
	if perm == "deny" {
		return &ToolResult{
			Content: fmt.Sprintf("%s tool is disabled for this agent", toolName),
			Success: false,
			Denied:  true,
		}
	}
	return nil
}

func (r *Registry) GetAll() []Tool {
	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

func (r *Registry) GetToolDefinitions() []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": t.Name, "description": t.Description, "parameters": t.Schema},
		})
	}
	return result
}


func (r *Registry) buildLogString(toolName string, args map[string]interface{}) string {
	switch toolName {
	case "read", "write", "edit", "patch":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "list":
		if p, ok := args["path"].(string); ok {
			return p
		}
	case "grep":
		if p, ok := args["path"].(string); ok {
			if pat, ok2 := args["pattern"].(string); ok2 {
				return p + " " + pat
			}
			return p
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "webfetch":
		if u, ok := args["url"].(string); ok {
			return u
		}
	case "websearch":
		if q, ok := args["query"].(string); ok {
			return q
		}
	}
	return ""
}

func (r *Registry) ChangePoint(name string, jasonarg string) (string, map[string]interface{}) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(jasonarg), &args); err != nil {
		return fmt.Errorf("parse args: %w", err).Error(), nil
	}
	// Case-insensitive tool matching
	nameLower := strings.ToLower(name)
	for key := range r.tools {
		if strings.ToLower(key) == nameLower {
			name = key
			break
		}
	}

	// Build log string with most important parameter
	return r.buildLogString(name, args), args
}

func (r *Registry) Execute(name string, args map[string]interface{}) (*ToolResult, error) {

	switch name {
	case "write":
		result, err := r.writeFile(args)
		return result, err
	case "read":
		result, err := r.readFile(args)
		return result, err
	case "bash":
		result, err := r.bashCommand(args)
		return result, err
	case "list":
		result, err := r.list(args)
		return result, err
	case "edit":
		result, err := r.editFile(args)
		return result, err
	case "grep":
		result, err := r.grepSearch(args)
		return result, err
	case "glob":
		result, err := r.globSearch(args)
		return result, err
	case "patch":
		result, err := r.applyPatch(args)
		return result, err
	case "webfetch":
		result, err := r.webFetch(args)
		return result, err
	case "websearch":
		result, err := r.webSearch(args)
		return result, err
	case "question":
		result, err := r.askQuestion(args)
		return result, err
	case "todowrite":
		result, err := r.todoWrite(args)
		return result, err
	case "switch_agent":
		result, err := r.switchAgent(args)
		return result, err
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// resolvePath converts a model-supplied path into the filesystem path used for
// operations. Relative paths are resolved against the workspace root so file
// operations never depend on the process working directory. Absolute paths are
// passed through unchanged (they still require explicit permission via
// checkPathPermission, which is the only place absolute paths are allowed).
func (r *Registry) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}

	p := filepath.Clean(filepath.FromSlash(path))

	// If the caller already included the workspace root as a prefix (e.g. the
	// model passes "output\file.go" while the output folder is "output"),
	// prepending the base again would produce a nested "output\output\file.go".
	// Compare absolute paths and only join when the given path lies outside the
	// base directory.
	if absBase, err := filepath.Abs(r.baseDir); err == nil {
		if absP, err := filepath.Abs(p); err == nil {
			if rel, err := filepath.Rel(absBase, absP); err == nil {
				if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
					return p
				}
			}
		}
	}

	return filepath.Join(r.baseDir, p)
}

// displayPath converts an operation path into the relative form shown to the
// model (relative to the workspace root). Absolute paths are only surfaced when
// the path cannot be expressed relative to the workspace.
func (r *Registry) displayPath(path string) string {
	rel, err := filepath.Rel(r.baseDir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func (r *Registry) writeFile(args map[string]interface{}) (*ToolResult, error) {
	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return &ToolResult{Success: false, Error: "file path is required"}, nil
	}

	content, ok := args["content"].(string)
	if !ok {
		return &ToolResult{Success: false, Error: "content is required"}, nil
	}

	if err := r.checkAgentPermission("write"); err != nil {
		return err, nil
	}

	fullPath := r.resolvePath(filePath)
	dir := filepath.Dir(fullPath)

	if err := r.checkPathPermission(filePath); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", filePath),
			Success: false,
			Denied:  true,
		}, nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("error write file: %w", err)
	}

	return &ToolResult{
		Content: fmt.Sprintf("successfully wrote file: %s", filePath),
		Success: true,
	}, nil
}

func (r *Registry) readFile(args map[string]interface{}) (*ToolResult, error) {
	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return &ToolResult{Success: false, Error: "file path is required"}, nil
	}

	fullPath := r.resolvePath(filePath)

	if err := r.checkPathPermission(filePath); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", filePath),
			Success: false,
			Denied:  true,
		}, nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error read file: %v", err),
			Success: false,
		}, nil
	}

	return &ToolResult{
		Content: string(data),
		Success: true,
	}, nil
}

func (r *Registry) bashCommand(args map[string]interface{}) (*ToolResult, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return &ToolResult{Success: false, Error: "command is required"}, nil
	}

	if err := r.checkAgentPermission("bash"); err != nil {
		return err, nil
	}

	// Destructive apps (shutdown*, rm*, del*) are never executed for real:
	// report a simulated success instead.
	if isFakeCommand(command) {
		return &ToolResult{
			Content: fmt.Sprintf("Command executed successfully: %s", command),
			Success: true,
		}, nil
	}

	// Extract and check paths
	paths := extractPathsFromCommand(command)
	for _, path := range paths {
		if err := r.checkPathPermission(path); err != nil {
			return &ToolResult{
				Content: fmt.Sprintf("path denied: %s", path),
				Success: false,
				Denied:  true,
			}, nil
		}
	}

	return r.executor.Run(command)
}

func (r *Registry) list(args map[string]interface{}) (*ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	fullPath := r.resolvePath(path)

	if err := r.checkPathPermission(path); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", path),
			Success: false,
			Denied:  true,
		}, nil
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error listing path: %v", err),
			Success: false,
		}, nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Directory: %s\n\n", r.displayPath(fullPath)))
	for _, entry := range entries {
		info, err := entry.Info()
		size := int64(0)
		if err == nil {
			size = info.Size()
		}
		mode := entry.Type().String()
		if entry.Type().IsDir() {
			mode = "DIR"
		} else {
			mode = "FILE"
		}
		result.WriteString(fmt.Sprintf("[%s] %s  (%d bytes)\n", mode, entry.Name(), size))
	}

	return &ToolResult{
		Content: result.String(),
		Success: true,
	}, nil
}

func (r *Registry) editFile(args map[string]interface{}) (*ToolResult, error) {
	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return &ToolResult{Success: false, Error: "file path is required"}, nil
	}

	oldString, ok := args["old_string"].(string)
	if !ok || oldString == "" {
		return &ToolResult{Success: false, Error: "old string is required"}, nil
	}

	newString, ok := args["new_string"].(string)
	if !ok {
		return &ToolResult{Success: false, Error: "new string is required"}, nil
	}

	if err := r.checkAgentPermission("edit"); err != nil {
		return err, nil
	}

	fullPath := r.resolvePath(filePath)

	if err := r.checkPathPermission(filePath); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", filePath),
			Success: false,
			Denied:  true,
		}, nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error read file: %v", err),
			Success: false,
		}, nil
	}

	// Case-insensitive replacement
	if !strings.Contains(string(data), oldString) {
		// Try case-insensitive
		lowerData := strings.ToLower(string(data))
		lowerOld := strings.ToLower(oldString)
		idx := strings.Index(lowerData, lowerOld)
		if idx == -1 {
			return &ToolResult{
				Content: fmt.Sprintf("old string not found in file: %s", oldString[:min(len(oldString), 100)]),
				Success: false,
			}, nil
		}
		// Replace with exact case from file
		data = append(data[:idx], append([]byte(newString), data[idx+len(oldString):]...)...)
	} else {
		data = []byte(strings.Replace(string(data), oldString, newString, 1))
	}

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return nil, fmt.Errorf("error write file: %w", err)
	}

	var result strings.Builder
	result.WriteString("edited: ")
	if len(filePath) > 80 {
		result.WriteString("..." + filePath[len(filePath)-77:])
	} else {
		result.WriteString(filePath)
	}
	result.WriteString("\n---\n")
	result.WriteString("old:\n")
	if len(oldString) > 500 {
		result.WriteString(oldString[:500] + "...")
	} else {
		result.WriteString(oldString)
	}
	result.WriteString("\nnew:\n")
	if len(newString) > 500 {
		result.WriteString(newString[:500] + "...")
	} else {
		result.WriteString(newString)
	}

	return &ToolResult{
		Content: result.String(),
		Success: true,
	}, nil
}

func (r *Registry) grepSearch(args map[string]interface{}) (*ToolResult, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return &ToolResult{Success: false, Error: "pattern is required"}, nil
	}

	path, ok := args["path"].(string)
	if !ok || path == "" {
		return &ToolResult{Success: false, Error: "path is required"}, nil
	}

	fullPath := r.resolvePath(path)

	if err := r.checkPathPermission(path); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", path),
			Success: false,
		}, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error regex pattern: %v", err),
			Success: false,
		}, nil
	}

	var result strings.Builder
	info, err := os.Stat(fullPath)
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("error: %v", err), Success: false}, nil
	}

	if !info.IsDir() {
		// Single file
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return &ToolResult{Content: fmt.Sprintf("error read file: %v", err), Success: false}, nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				result.WriteString(fmt.Sprintf("%s:%d:%s\n", path, i+1, line))
			}
		}
	} else {
		// Directory - walk
		filepath.Walk(fullPath, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				if info.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			relPath, _ := filepath.Rel(r.baseDir, p)
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if re.MatchString(line) {
					result.WriteString(fmt.Sprintf("%s:%d:%s\n", relPath, i+1, line))
				}
			}
			return nil
		})
	}

	if result.Len() == 0 {
		return &ToolResult{
			Content: "no matches found",
			Success: true,
		}, nil
	}

	return &ToolResult{
		Content: result.String(),
		Success: true,
	}, nil
}

func (r *Registry) globSearch(args map[string]interface{}) (*ToolResult, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return &ToolResult{Success: false, Error: "pattern is required"}, nil
	}

	matches, err := r.globRecursive(r.baseDir, pattern)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error glob pattern: %v", err),
			Success: false,
		}, nil
	}

	var result strings.Builder
	for _, match := range matches {
		relPath, _ := filepath.Rel(r.baseDir, match)
		result.WriteString(relPath + "\n")
	}

	if result.Len() == 0 {
		return &ToolResult{
			Content: "no matches found",
			Success: true,
		}, nil
	}

	return &ToolResult{
		Content: result.String(),
		Success: true,
	}, nil
}

// globRecursive performs recursive glob matching supporting ** patterns.
// ** matches any number of directories (including zero).
func (r *Registry) globRecursive(baseDir, pattern string) ([]string, error) {
	// If pattern is absolute, search from root; otherwise search from baseDir.
	searchDir := baseDir
	p := pattern
	if filepath.IsAbs(pattern) {
		searchDir = filepath.Dir(pattern)
		p = filepath.Base(pattern)
	}

	parts := splitGlobPattern(p)
	return r.globWalk(searchDir, parts, 0)
}

// splitGlobPattern splits a glob pattern into parts, handling ** as a special token.
func splitGlobPattern(pattern string) []string {
	parts := strings.Split(pattern, string(filepath.Separator))
	var result []string
	for _, part := range parts {
		if part == "**" {
			result = append(result, "**")
		} else {
			result = append(result, part)
		}
	}
	return result
}

// globWalk recursively walks the directory matching pattern parts.
func (r *Registry) globWalk(dir string, parts []string, partIdx int) ([]string, error) {
	if partIdx >= len(parts) {
		return nil, nil
	}

	part := parts[partIdx]

	if part == "**" {
		// ** matches zero or more directories.
		// Try matching the rest of the pattern from this directory (zero match).
		result, err := r.globWalk(dir, parts, partIdx+1)
		if err != nil {
			return nil, err
		}

		// Try matching the rest from each subdirectory (one or more matches).
		entries, err := os.ReadDir(dir)
		if err != nil {
			return result, nil
		}
		for _, entry := range entries {
			if entry.IsDir() {
				subDir := filepath.Join(dir, entry.Name())
				subResult, err := r.globWalk(subDir, parts, partIdx+1)
				if err != nil {
					return nil, err
				}
				result = append(result, subResult...)
			}
		}
		return result, nil
	}

	// Regular part: match against directory entries.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var result []string
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())

		matched, err := filepath.Match(part, entry.Name())
		if err != nil {
			return nil, err
		}

		if !matched {
			continue
		}

		if partIdx == len(parts)-1 {
			// Last part: if it's a file (or matches file pattern), add it.
			if !entry.IsDir() || strings.Contains(part, "*") {
				result = append(result, fullPath)
			}
		} else {
			// Not last part: must be a directory to continue walking.
			if entry.IsDir() {
				subResult, err := r.globWalk(fullPath, parts, partIdx+1)
				if err != nil {
					return nil, err
				}
				result = append(result, subResult...)
			}
		}
	}

	return result, nil
}

func (r *Registry) applyPatch(args map[string]interface{}) (*ToolResult, error) {
	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return &ToolResult{Success: false, Error: "file path is required"}, nil
	}

	patch, ok := args["patch"].(string)
	if !ok || patch == "" {
		return &ToolResult{Success: false, Error: "patch is required"}, nil
	}

	if err := r.checkAgentPermission("patch"); err != nil {
		return err, nil
	}

	fullPath := r.resolvePath(filePath)

	if err := r.checkPathPermission(filePath); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("path denied: %s", filePath),
			Success: false,
			Denied:  true,
		}, nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error read file: %v", err),
			Success: false,
		}, nil
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	patch = strings.ReplaceAll(patch, "\r\n", "\n")

	hunks := parseHunks(patch)
	if len(hunks) == 0 {
		return &ToolResult{
			Content: "no hunks found in patch",
			Success: false,
		}, nil
	}

	var result []string
	oldIdx := 0
	for _, h := range hunks {
		start := h.OldStart - 1 // hunk header is 1-based
		if start < oldIdx {
			return &ToolResult{
				Content: "patch hunks out of order or overlapping",
				Success: false,
			}, nil
		}
		// Copy untouched lines up to the hunk start.
		for ; oldIdx < start; oldIdx++ {
			if oldIdx < len(lines) {
				result = append(result, lines[oldIdx])
			}
		}
		// Apply the hunk body.
		for _, pline := range h.Body {
			switch {
			case strings.HasPrefix(pline, " "):
				if oldIdx >= len(lines) || lines[oldIdx] != pline[1:] {
					return &ToolResult{
						Content: fmt.Sprintf("patch failed: context mismatch at line %d", oldIdx+1),
						Success: false,
					}, nil
				}
				result = append(result, lines[oldIdx])
				oldIdx++
			case strings.HasPrefix(pline, "-"):
				if oldIdx >= len(lines) || lines[oldIdx] != pline[1:] {
					return &ToolResult{
						Content: fmt.Sprintf("patch failed: deletion mismatch at line %d", oldIdx+1),
						Success: false,
					}, nil
				}
				oldIdx++
			case strings.HasPrefix(pline, "+"):
				result = append(result, pline[1:])
			case strings.HasPrefix(pline, "\\"):
				// "\ No newline at end of file" marker: ignore.
			default:
				return &ToolResult{
					Content: fmt.Sprintf("patch failed: unexpected patch line %q", pline),
					Success: false,
				}, nil
			}
		}
	}
	// Copy the remaining untouched lines.
	for ; oldIdx < len(lines); oldIdx++ {
		result = append(result, lines[oldIdx])
	}

	if err := os.WriteFile(fullPath, []byte(strings.Join(result, "\n")), 0644); err != nil {
		return nil, fmt.Errorf("error patched file: %w", err)
	}

	return &ToolResult{
		Content: fmt.Sprintf("%s", filePath),
		Success: true,
	}, nil
}

// hunk is one unified-diff hunk with its 1-based start positions.
type hunk struct {
	OldStart int
	NewStart int
	Body     []string
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseHunks splits a unified diff into hunks, honouring the "@@" headers.
func parseHunks(patch string) []hunk {
	var hunks []hunk
	lines := strings.Split(patch, "\n")
	for i := 0; i < len(lines); i++ {
		m := hunkHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		h := hunk{}
		fmt.Sscanf(m[1], "%d", &h.OldStart)
		fmt.Sscanf(m[2], "%d", &h.NewStart)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "@@") {
				break
			}
			if lines[j] == "" {
				// Trailing blank produced by the final newline of the patch
				// string; genuine blank context lines are written as " ".
				continue
			}
			h.Body = append(h.Body, lines[j])
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// maxWebBodyBytes caps how much remote content we read into memory.
const maxWebBodyBytes = 1 << 20 // 1 MiB

// heavyTextThreshold is the minimum length (in bytes) above which fetched web
// content is compressed with compressHeavyText. Short pages are returned as-is.
const heavyTextThreshold = 12000

func (r *Registry) webFetch(args map[string]interface{}) (*ToolResult, error) {
	u, ok := args["url"].(string)
	if !ok || u == "" {
		return &ToolResult{Success: false, Error: "url is required"}, nil
	}

	if parsed, err := url.Parse(u); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return &ToolResult{
			Content: "only http/https are allowed",
			Success: false,
		}, nil
	}

	resp, err := r.webClient.Get(u)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error fetching: %v", err),
			Success: false,
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &ToolResult{
			Content: fmt.Sprintf("http error: %d", resp.StatusCode),
			Success: false,
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBodyBytes+1))
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error read response: %v", err),
			Success: false,
		}, nil
	}
	if len(body) > maxWebBodyBytes {
		return &ToolResult{
			Content: "response exceeds limit",
			Success: false,
		}, nil
	}

	content := stripHTMLTags(string(body))

	if r.compressWebText && len(content) > heavyTextThreshold {
		content = compressHeavyText(content)
	}

	return &ToolResult{
		Content: content,
		Success: true,
	}, nil
}

var stopWords = map[string]bool{
	"и": true, "в": true, "во": true, "что": true, "он": true, "на": true, "но": true, "да": true,
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true, "is": true, "are": true,
}

// compressHeavyText сжимает большие тексты (~20КБ) через фильтрацию предложений и стоп-слов
func compressHeavyText(input string) string {
	paragraphs := strings.Split(input, "\n")
	var compressedParagraphs []string

	sentenceReg := regexp.MustCompile(`[^.!?]+[.!?]?`)

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		sentences := sentenceReg.FindAllString(para, -1)
		var important []string

		if len(sentences) > 3 {
			important = append(important, sentences[0], sentences[1])
			important = append(important, sentences[len(sentences)-1])
		} else {
			important = sentences
		}

		var cleanWords []string
		for _, sentence := range important {
			lowered := strings.ToLower(sentence)
			reg := regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
			lowered = reg.ReplaceAllString(lowered, "")

			words := strings.Fields(lowered)
			for _, word := range words {
				if !stopWords[word] {
					cleanWords = append(cleanWords, word)
				}
			}
		}

		if len(cleanWords) > 0 {
			compressedParagraphs = append(compressedParagraphs, strings.Join(cleanWords, " "))
		}
	}

	return strings.Join(compressedParagraphs, " \n ")
}

func stripHTMLTags(html string) string {
	if len(html) == 0 {
		return ""
	}

	for i := 0; i < len(html); i++ {
		b := html[i]
		if b < 0x09 && b != 0x0A && b != 0x0D {
			return ""
		}
	}

	// Drop script/style/noscript/template blocks entirely: their text content
	// is not readable page text and can be large or misleading.
	reBlock := regexp.MustCompile(`(?is)<(script|style|noscript|template)[^>]*>.*?</(script|style|noscript|template)>`)
	html = reBlock.ReplaceAllString(html, " ")

	lineBreakTags := []string{
		`<br\s*/?>`,
		`</p>`,
		`</div>`,
		`</li>`,
		`</tr>`,
		`</td>`,
		`</th>`,
		`</h[1-6]>`,
		`</blockquote>`,
		`</pre>`,
		`</section>`,
		`</article>`,
		`</header>`,
		`</footer>`,
		`</nav>`,
		`</aside>`,
		`</main>`,
		`</figure>`,
		`</figcaption>`,
		`</details>`,
		`</summary>`,
	}

	for _, tag := range lineBreakTags {
		re := regexp.MustCompile(`(?i)` + tag)
		html = re.ReplaceAllString(html, "\n")
	}

	re := regexp.MustCompile(`<[^>]*>`)
	html = re.ReplaceAllString(html, " ")

	re2 := regexp.MustCompile(`[ \t]+`)
	html = re2.ReplaceAllString(html, " ")

	html = strings.TrimSpace(html)

	re3 := regexp.MustCompile(`\r?\n[ \t]+\r?\n`)
	html = re3.ReplaceAllString(html, "\n\n")

	re4 := regexp.MustCompile(`\r?\n\r?\n`)
	html = re4.ReplaceAllString(html, "\n\n")

	html = strings.TrimSpace(html)

	if len(html) > 20000 {
		html = html[:20000]
	}

	return html
}

func (r *Registry) webSearch(args map[string]interface{}) (*ToolResult, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return &ToolResult{Success: false, Error: "query is required"}, nil
	}

	// Simple web search using DuckDuckGo HTML
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))
	resp, err := r.webClient.Get(searchURL)
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error searching: %v", err),
			Success: false,
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBodyBytes+1))
	if err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error reading response: %v", err),
			Success: false,
		}, nil
	}

	// Simple extraction of results
	results := extractDuckDuckGoResults(string(body), r.webfetchLimit)
	if results == "" {
		results = "no results found"
	}

	return &ToolResult{
		Content: results,
		Success: true,
	}, nil
}
func extractDuckDuckGoResults(html string, limit int) string {
	var results strings.Builder

	// Simple regex-based extraction
	re := regexp.MustCompile(`<a rel="nofollow" class="result__a" href="([^"]+)">([^<]+)</a>`)
	matches := re.FindAllStringSubmatch(html, -1)

	for i, m := range matches {
		if i >= limit {
			break
		}
		if len(m) >= 3 {
			results.WriteString(fmt.Sprintf("%s\n  %s\n\n", m[2], m[1]))
		}
	}

	if results.Len() == 0 {
		return "Web search results:\n" + html[:min(1000, len(html))]
	}

	return "Web search results:\n" + results.String()
}

func (r *Registry) askQuestion(args map[string]interface{}) (*ToolResult, error) {
	question, ok := args["question"].(string)
	if !ok || question == "" {
		return &ToolResult{Success: false, Error: "question is required"}, nil
	}

	optionsStr, _ := args["options"].(string)
	var options []string
	if optionsStr != "" {
		parts := strings.Split(optionsStr, ",")
		for _, p := range parts {
			options = append(options, strings.TrimSpace(p))
		}
	}

	var answer string
	if r.onQuestionAsk != nil {
		var err error
		answer, err = r.onQuestionAsk(question, options)
		if err != nil {
			return &ToolResult{
				Content: fmt.Sprintf("error asking question: %v", err),
				Success: false,
			}, nil
		}
	} else {
		answer = "no answer callback"
	}

	return &ToolResult{
		Content: fmt.Sprintf("question: %s\nanswer: %s", question, answer),
		Success: true,
	}, nil
}

func (r *Registry) todoWrite(args map[string]interface{}) (*ToolResult, error) {
	todos, ok := args["todos"].(string)
	if !ok || todos == "" {
		return &ToolResult{Success: false, Error: "todos is required"}, nil
	}

	// Validate JSON
	var todoList []interface{}
	if err := json.Unmarshal([]byte(todos), &todoList); err != nil {
		return &ToolResult{
			Content: fmt.Sprintf("error todos json: %v", err),
			Success: false,
		}, nil
	}

	var result strings.Builder
	for i, item := range todoList {
		if m, ok := item.(map[string]interface{}); ok {
			desc := ""
			if d, ok := m["description"].(string); ok {
				desc = d
			} else if d, ok := m["content"].(string); ok {
				desc = d
			}
			if desc != "" {
				var status string
				if s, ok := m["status"].(string); ok {
					status = s
				} else {
					status = "pending"
				}
				result.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, status, desc))
			}
		}
	}

	return &ToolResult{
		Content: result.String(),
		Success: true,
	}, nil
}

func (r *Registry) switchAgent(args map[string]interface{}) (*ToolResult, error) {
	agentName, ok := args["agent_name"].(string)
	if !ok || agentName == "" {
		return &ToolResult{Success: false, Error: "agent_name is required"}, nil
	}

	if r.onSwitchAgent != nil {
		resultMsg, err := r.onSwitchAgent(agentName)
		if err != nil {
			return &ToolResult{
				Content: fmt.Sprintf("switch error: %v", err),
				Success: false,
			}, nil
		}
		return &ToolResult{
			Content: resultMsg,
			Success: true,
		}, nil
	}

	return &ToolResult{
		Content: fmt.Sprintf("switch_agent tool called with agent_name=%s (no handler registered)", agentName),
		Success: true,
	}, nil
}

func (r *Registry) checkPathPermission(path string) error {
	normalizedPath := normalizePath(path)
	// Resolve relative paths against the output folder so permission checks
	// match where file operations actually land (see resolvePath).
	if !filepath.IsAbs(path) && !strings.HasPrefix(normalizedPath, "/") &&
		!(len(normalizedPath) >= 2 && normalizedPath[1] == ':') {
		normalizedPath = normalizePath(r.resolvePath(path))
	}
	normalizedBase := normalizePath(r.baseDir)

	needsPermission := false

	if strings.HasPrefix(normalizedPath, "/") {
		needsPermission = true
	}

	if strings.Contains(normalizedPath, "../") || strings.Contains(normalizedPath, "/..") {
		needsPermission = true
	}

	if !needsPermission {
		absPath, err := filepath.Abs(normalizedPath)
		if err != nil {
			return err
		}
		absPath = normalizePath(absPath)

		absBase, err := filepath.Abs(normalizedBase)
		if err != nil {
			return err
		}
		absBase = normalizePath(absBase)

		// 1. Inside base directory → allowed immediately
		if strings.HasPrefix(absPath, absBase) || absPath == absBase {
			return nil
		}

		// 2/3. Check denied and allowed paths (only for paths outside base)
		r.permMu.Lock()
		if r.deniedPaths[normalizePath(absPath)] {
			r.permMu.Unlock()
			return fmt.Errorf("path denied: %s", absPath)
		}
		if r.allowedPaths[normalizePath(absPath)] {
			r.permMu.Unlock()
			return nil
		}
		r.permMu.Unlock()

		// 4. Needs user permission
		needsPermission = true
	}

	if r.onPathAsk != nil {
		absPath, err := filepath.Abs(normalizedPath)
		if err != nil {
			return err
		}
		absPath = normalizePath(absPath)

		result, err := r.onPathAsk(absPath)
		if err != nil {
			return err
		}

		r.permMu.Lock()
		switch result {
		case PermissionAllow:
			r.allowedPaths[normalizePath(absPath)] = true
		case PermissionAllowAll:
			r.allowedPaths[normalizePath(absPath)] = true
		case PermissionDeny:
			r.deniedPaths[normalizePath(absPath)] = true
		case PermissionDenyAll:
			r.deniedPaths[normalizePath(absPath)] = true
		}
		persist := result == PermissionAllowAll || result == PermissionDeny || result == PermissionDenyAll
		r.permMu.Unlock()

		if persist {
			r.savePersistentPermissions(r.permDir)
		}

		switch result {
		case PermissionDeny, PermissionDenyAll:
			return fmt.Errorf("path denied: %s", absPath)
		}
	}

	return nil
}

func normalizePath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")

	// Collapse repeated slashes. ReplaceAll is non-overlapping, so repeat until
	// stable (turns "a//b///c" into "a/b/c").
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}

	leadingSlash := strings.HasPrefix(path, "/")

	parts := strings.Split(path, "/")
	var out []string
	for _, p := range parts {
		switch p {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				last := out[len(out)-1]
				// Do not pop above a Windows drive root or keep descending past
				// already-protected ".." entries.
				if last != ".." && !(len(last) == 2 && last[1] == ':') {
					out = out[:len(out)-1]
					continue
				}
			}
			out = append(out, "..")
		default:
			out = append(out, p)
		}
	}

	result := strings.Join(out, "/")
	if leadingSlash {
		result = "/" + result
	}
	return result
}

// isFakeApp reports whether an app name belongs to the destructive set
// (shutdown*, rm*, del*).
func isFakeApp(name string) bool {
	name = strings.ToLower(strings.Trim(name, `"'`))
	for _, p := range fakeAppPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// isFakeCommand reports whether a shell command launches a destructive app
// (shutdown*, rm*, del*), including through shell wrappers such as
// `cmd /C del file` or `sh -c "rm x"`. Such commands are simulated, not
// executed.
func isFakeCommand(command string) bool {
	for _, seg := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '&' || r == '|' || r == ';' || r == '\n' || r == '\r'
	}) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		if isFakeApp(fields[0]) {
			return true
		}
		// Unwrap shell wrappers: cmd /C del x, sh -c "rm x", powershell -Command "del x".
		for i := 1; i < len(fields); i++ {
			switch strings.ToLower(fields[i]) {
			case "/c", "-c", "-command", "-cmd":
				if i+1 < len(fields) && isFakeApp(fields[i+1]) {
					return true
				}
			}
		}
	}
	return false
}

func (e *ShellExecutor) Run(command string) (*ToolResult, error) {
	// A background context is used when no timeout is configured so
	// CommandContext behaves exactly like Command.
	ctx := context.Background()
	var cancel context.CancelFunc
	if e.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(e.Timeout)*time.Second)
		defer cancel()
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if e.Dir != "" {
		cmd.Dir = e.Dir
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if e.Timeout > 0 && ctx.Err() == context.DeadlineExceeded {
			return &ToolResult{
				Content: fmt.Sprintf("Command timed out after %d seconds", e.Timeout),
				Success: false,
				Error:   "command timed out",
			}, nil
		}
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return &ToolResult{
			Content: fmt.Sprintf("Command failed with exit code %d\nOutput:\n%s", exitCode, string(output)),
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &ToolResult{
		Content: string(output),
		Success: true,
	}, nil
}

func extractPathsFromCommand(command string) []string {
	var paths []string

	cleaned := strings.TrimSpace(command)

	if strings.HasPrefix(cleaned, "cd ") {
		parts := strings.SplitN(cleaned[3:], " ", 2)
		paths = append(paths, strings.TrimSpace(parts[0]))
	}

	pathPatterns := []string{
		`(?:-o|--output|-O)\s+(\S+)`,
		`(?:-I|-i|--input)\s+(\S+)`,
		`(?:cp|mv|mkdir|rm|rm -rf)\s+.*?(\S+)`,
		`(?:cat|less|head|tail|wc)\s+(\S+)`,
		`(?:make|cmake|ninja)\s+-C\s+(\S+)`,
		`go\s+run\s+(\S+)`,
		`go\s+build\s+-o\s+(\S+)\s+\S+`,
		`npm\s+(?:install|run|start)\s+.*?(?:--prefix|--prefix=)(\S+)`,
		`python\s+(\S+\.py)`,
		`node\s+(\S+)`,
		`gcc|g\+\+|clang|clang\+\+.*?\s+(?:-o|--output)\s+(\S+)`,
		`rustc\s+(\S+)`,
	}

	for _, pattern := range pathPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(command, -1)
		for _, m := range matches {
			if len(m) > 1 {
				p := m[1]
				p = regexp.MustCompile(`[*?].*$`).ReplaceAllString(p, "")
				if p != "" {
					paths = append(paths, p)
				}
			}
		}
	}

	dirRe := regexp.MustCompile(`(?:WORKDIR|workdir|dir)\s+(\S+)`)
	matches := dirRe.FindAllStringSubmatch(command, -1)
	for _, m := range matches {
		if len(m) > 1 {
			p := m[1]
			p = regexp.MustCompile(`[*?].*$`).ReplaceAllString(p, "")
			if p != "" {
				paths = append(paths, p)
			}
		}
	}

	return paths
}
