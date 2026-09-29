package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-coder-agent/agents"
	"ai-coder-agent/api"
	"ai-coder-agent/config"
	"ai-coder-agent/logger"
)

func newTestAgent(t *testing.T, dir string, handler http.HandlerFunc) (*Agent, *config.Config) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	mgr := agents.NewManager()
	mgr.LoadBuiltIn()

	cfg := config.Default()
	cfg.APIKey = "k"
	cfg.BaseURL = srv.URL
	cfg.Model = "m"
	cfg.OutputFolder = dir
	cfg.MaxTokens = 4096
	cfg.Temperature = 0.2
	cfg.WebSearchLimit = 0
	cfg.WebFetchLimit = 0

	log, err := logger.New(filepath.Join(dir, "test.log"), false, 1024*1024)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(log.Close)

	a, err := New(cfg, log, mgr)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	return a, cfg
}

func writeCompletion(t *testing.T, w http.ResponseWriter, content interface{}, toolCalls []map[string]interface{}) {
	t.Helper()
	msg := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	resp := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     5,
			"completion_tokens": 5,
			"total_tokens":      10,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func toolCallJSON(t *testing.T, name, args string) []map[string]interface{} {
	t.Helper()
	return []map[string]interface{}{
		{
			"id":       "call_1",
			"type":     "function",
			"function": map[string]interface{}{"name": name, "arguments": args},
		},
	}
}

func TestProcessMessageFinalAnswer(t *testing.T) {
	dir := t.TempDir()
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(t, w, "mock answer", nil)
	})
	if err := a.ProcessMessage("hi"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if a.MessageCount() != 3 { // system + user + assistant
		t.Errorf("message count = %d, want 3", a.MessageCount())
	}
}

func TestProcessMessageToolLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := 0
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			writeCompletion(t, w, nil, toolCallJSON(t, "read", `{"file_path":"hello.txt"}`))
			return
		}
		writeCompletion(t, w, "done reading", nil)
	})

	if err := a.ProcessMessage("read the file"); err != nil {
		t.Fatalf("process: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("api calls = %d, want 2", calls)
	}
}

func TestVerifyConnection(t *testing.T) {
	dir := t.TempDir()
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(t, w, "ok", nil)
	})
	if err := a.VerifyConnection(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestProcessMessageContentNotString(t *testing.T) {
	dir := t.TempDir()
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		// Non-string content must not panic the agent.
		writeCompletion(t, w, []interface{}{"weird", "content"}, nil)
	})
	if err := a.ProcessMessage("hi"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := fmt.Sprintf("%v", a.messages[len(a.messages)-1].Content); got == "" {
		t.Error("expected content to be stored")
	}
}

func TestCompactSession(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	calls := 0
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			writeCompletion(t, w, "text reply", nil)
			return
		}
		writeCompletion(t, w, "summary text", nil)
	})
	if err := a.ProcessMessage("hello"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if err := a.CompactSession(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if a.MessageCount() != 1 {
		t.Errorf("message count after compact = %d, want 1 (system only)", a.MessageCount())
	}
}

func TestTranscriptSkipsSystemAndTools(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	calls := 0
	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			writeCompletion(t, w, nil, toolCallJSON(t, "read", `{"file_path":"nope.txt"}`))
			return
		}
		writeCompletion(t, w, "final answer", nil)
	})
	if err := a.ProcessMessage("do something"); err != nil {
		t.Fatalf("process: %v", err)
	}
}

// TestContainsUserQuery verifies the guard that multi-step tool chat templates
// (e.g. Qwen3.5) rely on: a request must contain a genuine user turn.
func TestContainsUserQuery(t *testing.T) {
	cases := []struct {
		name string
		msgs []api.Message
		want bool
	}{
		{"user present", []api.Message{{Role: "user", Content: "hi"}}, true},
		{"system only", []api.Message{{Role: "system", Content: "x"}}, false},
		{"tool only", []api.Message{{Role: "tool", Content: "r", ToolCallID: "c"}}, false},
		{"empty user", []api.Message{{Role: "user", Content: ""}}, false},
		{"tool response wrapper", []api.Message{{Role: "user", Content: "<tool_response>\noops\n</tool_response>"}}, false},
		{"mixed with real user", []api.Message{
			{Role: "tool", Content: "r", ToolCallID: "c"},
			{Role: "user", Content: "<tool_response>x</tool_response>"},
			{Role: "user", Content: "real query"},
		}, true},
	}
	for _, tc := range cases {
		if got := containsUserQuery(tc.msgs); got != tc.want {
			t.Errorf("%s: containsUserQuery = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRunLoopReaddsUserQuery ensures a request is never sent without a user
// turn: after compaction a session can hold only tool/assistant messages, and
// the loop must re-inject a user message before calling the API.
func TestRunLoopReaddsUserQuery(t *testing.T) {
	dir := t.TempDir()
	sent := make(chan []string, 1)

	a, _ := newTestAgent(t, dir, func(w http.ResponseWriter, r *http.Request) {
		var body api.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var roles []string
		for _, m := range body.Messages {
			roles = append(roles, m.Role)
		}
		sent <- roles
		writeCompletion(t, w, "final answer", nil)
	})

	// Simulate the post-compaction state of a running tool loop: the original
	// user query was replaced by a summary, leaving no user turn.
	a.messages = []api.Message{
		{Role: "system", Content: "summary"},
		{Role: "assistant", ToolCalls: []api.ToolCall{{
			ID: "call_1", Type: "function",
			Function: api.ToolCallFunction{Name: "read", Arguments: `{"file_path":"x.txt"}`},
		}}},
		{Role: "tool", Content: "not found", ToolCallID: "call_1"},
	}

	var total time.Duration
	if err := a.runLoop(&total); err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	roles := <-sent
	if !containsString(roles, "user") {
		t.Errorf("request roles %v must contain a user message", roles)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
