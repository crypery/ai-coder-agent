package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"ai-coder-agent/agent"
	"ai-coder-agent/agents"
	"ai-coder-agent/config"
	"ai-coder-agent/logger"
)

const mockCompletionJSON = `{"choices":[{"index":0,"message":{"role":"assistant","content":"mock answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()

	apiSRV := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockCompletionJSON))
	}))
	t.Cleanup(apiSRV.Close)

	mgr := agents.NewManager()
	mgr.LoadBuiltIn()

	cfg := config.Default()
	cfg.APIKey = "k"
	cfg.BaseURL = apiSRV.URL
	cfg.Model = "m"
	cfg.OutputFolder = dir
	cfg.WebSearchLimit = 0
	cfg.WebFetchLimit = 0
	cfg.MaxTokens = 1024

	log, err := logger.New(filepath.Join(dir, "test.log"), false, 1024*1024)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(log.Close)

	a, err := agent.New(cfg, log, mgr)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	return New(a, mgr, cfg, log)
}

func TestSplitTitle(t *testing.T) {
	title, content := splitTitle("read_file\n{\"file_path\": \"a.go\"}")
	if title != "read_file" || content != `{"file_path": "a.go"}` {
		t.Errorf("splitTitle = %q / %q", title, content)
	}
	title, content = splitTitle("no newline")
	if title != "no newline" || content != "" {
		t.Errorf("splitTitle single line = %q / %q", title, content)
	}
}

func TestToolHeader(t *testing.T) {
	cases := []struct {
		name, target, want string
	}{
		{"read", "src/main.go", "read : src/main.go"},
		{"edit", "f.txt", "edit : f.txt"},
		{"bash", "go build ./...", "bash : go build ./..."},
		{"grep", "sub\npattern here", "grep : sub"},
		{"webfetch", "https://example.com/x", "webfetch : https://example.com/x"},
		{"todowrite", "", "todowrite"},
	}
	for _, tc := range cases {
		if got := toolHeader(tc.name, tc.target); got != tc.want {
			t.Errorf("toolHeader(%q, %q) = %q, want %q", tc.name, tc.target, got, tc.want)
		}
	}
}

func TestIndexHandler(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "AI Agent Chat") {
		t.Error("index does not contain title 'AI Agent Chat'")
	}
	if !strings.Contains(body, "localStorage") {
		t.Error("index should contain the theme toggle using localStorage")
	}
	if !strings.Contains(body, "bootstrap") || !strings.Contains(body, "jquery") {
		t.Error("index should reference bootstrap and jquery")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("content-type = %q, want text/html", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header = %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("frame options header = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("referrer policy header = %q", got)
	}
}

func TestNotFoundForUnknownPaths(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/admin", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCheckOrigin(t *testing.T) {
	if !checkOrigin(httptest.NewRequest("GET", "http://localhost/ws", nil)) {
		t.Error("request without Origin should be allowed")
	}
	req := httptest.NewRequest("GET", "http://localhost/ws", nil)
	req.Header.Set("Origin", "http://localhost")
	if !checkOrigin(req) {
		t.Error("matching Origin should be allowed")
	}
	req2 := httptest.NewRequest("GET", "http://localhost/ws", nil)
	req2.Header.Set("Origin", "http://evil.example")
	if checkOrigin(req2) {
		t.Error("cross-origin should be rejected")
	}
	req3 := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req3.Header.Set("Origin", "not a url")
	if checkOrigin(req3) {
		t.Error("malformed Origin should be rejected")
	}
	// Port must be taken into account.
	req4 := httptest.NewRequest("GET", "http://localhost/ws", nil)
	req4.Header.Set("Origin", "http://localhost:9999")
	if checkOrigin(req4) {
		t.Error("origin with different port should be rejected")
	}
}

func TestWebsocketChatFlow(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	if err := conn.WriteJSON(wsMessage{Type: "chat", Text: "hello web"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	gotUser, gotServer := false, false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		switch msg.Type {
		case "user":
			if msg.Text == "hello web" {
				gotUser = true
			}
		case "server":
			if msg.Text == "mock answer" {
				gotServer = true
			}
		}
		if gotUser && gotServer {
			return
		}
	}
	t.Fatalf("did not receive both user echo and mock answer (user=%v server=%v)", gotUser, gotServer)
}

func dialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func waitForServerMsg(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		switch msg.Type {
		case "user", "server", "system":
			if msg.Text == want {
				return
			}
		}
	}
	t.Fatalf("did not receive %q over websocket", want)
}

func readUntil(t *testing.T, conn *websocket.Conn, stop string, out *[]wsMessage) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		*out = append(*out, msg)
		switch msg.Type {
		case "user", "server", "system":
			if msg.Text == stop {
				return
			}
		}
	}
	t.Fatalf("did not receive %q over websocket", stop)
}

// Multiple tabs share one conversation: a message sent from one tab is echoed
// and answered on every tab.
func TestWebsocketSharedSessionAcrossTabs(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	tab1 := dialWS(t, wsURL)
	tab2 := dialWS(t, wsURL)

	// Both tabs may receive the "connected" banner; drain until banner seen.
	waitForServerMsg(t, tab1, "Connected to AI Agent.")
	waitForServerMsg(t, tab2, "Connected to AI Agent.")

	if err := tab1.WriteJSON(wsMessage{Type: "chat", Text: "hello multi-tab"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitForServerMsg(t, tab1, "hello multi-tab") // user echo
	waitForServerMsg(t, tab2, "hello multi-tab") // user echo on other tab
	waitForServerMsg(t, tab1, "mock answer")
	waitForServerMsg(t, tab2, "mock answer")
}

// History is stored in the browser (localStorage), so a newly opened tab does
// not receive the previous conversation from the server: the chat starts from
// the live broadcast plus the connect banner.
func TestWebsocketNoHistoryOnConnect(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	tab1 := dialWS(t, wsURL)
	waitForServerMsg(t, tab1, "Connected to AI Agent.")

	if err := tab1.WriteJSON(wsMessage{Type: "chat", Text: "first message"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForServerMsg(t, tab1, "mock answer")

	tab2 := dialWS(t, wsURL)
	var got []wsMessage
	readUntil(t, tab2, "Connected to AI Agent.", &got)
	for _, m := range got {
		if m.Text == "first message" || m.Text == "mock answer" {
			t.Fatalf("new tab received stale history message %q", m.Text)
		}
	}
}

// An unanswered permission prompt must not hang forever: after the configured
// timeout the request is auto-denied, the running tool reports Denied and the
// agent stops with a visible message. The prompt is triggered by a path
// outside the output folder.
func TestPermissionPromptTimesOutAndStopsRequest(t *testing.T) {
	dir := t.TempDir()

	callCount := 0
	const bashToolCallJSON = `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"cat /outside.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	apiSRV := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			_, _ = w.Write([]byte(bashToolCallJSON))
			return
		}
		_, _ = w.Write([]byte(mockCompletionJSON))
	}))
	t.Cleanup(apiSRV.Close)

	mgr := agents.NewManager()
	mgr.LoadBuiltIn()

	cfg := config.Default()
	cfg.APIKey = "k"
	cfg.BaseURL = apiSRV.URL
	cfg.Model = "m"
	cfg.OutputFolder = dir
	cfg.WebSearchLimit = 0
	cfg.WebFetchLimit = 0
	cfg.MaxTokens = 1024
	cfg.PermissionTimeout = 1

	log, err := logger.New(filepath.Join(dir, "test.log"), false, 1024*1024)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(log.Close)

	a, err := agent.New(cfg, log, mgr)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	s := New(a, mgr, cfg, log)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	tab := dialWS(t, wsURL)
	waitForServerMsg(t, tab, "Connected to AI Agent.")

	if err := tab.WriteJSON(wsMessage{Type: "chat", Text: "run something"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Without answering the prompt, the request must auto-deny and stop.
	waitForServerMsg(t, tab, "Request stopped: permission denied by the user.")
}