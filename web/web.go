// Package web implements an optional chat server that exposes the same
// functionality as the console mode through a browser UI. Communication uses
// websockets (github.com/gorilla/websocket) and the single-file UI is embedded
// into the binary via go:embed.
//
// All connected tabs share a single conversation: any message sent from any
// tab is processed by the same agent, and events are broadcast to every tab.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"ai-coder-agent/agent"
	"ai-coder-agent/agents"
	"ai-coder-agent/commands"
	"ai-coder-agent/config"
	"ai-coder-agent/logger"
	"ai-coder-agent/tools"

	"github.com/google/uuid"
)

//go:embed index.html
var embedded embed.FS

// Websocket message type names (client -> server).
const (
	typeChat       = "chat"
	typeAnswer     = "answer"
	typePermission = "permission"
	typeStop       = "stop"
)

// Websocket message type names (server -> client).
const (
	typeUser       = "user"
	typeServer     = "server"
	typeSystem     = "system"
	typeQuestion   = "question"
	typeAskPerm    = "permission"
	typeStats      = "stats"
	typeServerDone = "server_done"
)

// wsMessage is the JSON envelope used for every websocket frame. For typeSystem
// frames the logger event name and its human-readable title (the file path, URL
// or command a tool operated on) are sent as separate fields so the UI never
// has to re-parse the payload text.
type wsMessage struct {
	Type           string   `json:"type"`
	Event          string   `json:"event,omitempty"`
	Title          string   `json:"title,omitempty"`
	Text           string   `json:"text,omitempty"`
	ID             string   `json:"id,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Target         string   `json:"target,omitempty"`
	Question       string   `json:"question,omitempty"`
	Options        []string `json:"options,omitempty"`
	Decision       string   `json:"decision,omitempty"`
	Model          string   `json:"model,omitempty"`
	API            string   `json:"api,omitempty"`
	Output         string   `json:"output,omitempty"`
	Messages       int      `json:"messages,omitempty"`
	TokensPrompt   int      `json:"tokensPrompt,omitempty"`
	TokensComplete int      `json:"tokensCompletion,omitempty"`
	TokensTotal    int      `json:"tokensTotal,omitempty"`
	TokensMax      int      `json:"tokensMax,omitempty"`
	StopFallbackMs int      `json:"stopFallbackMs"`
	ReconnectMs    int      `json:"reconnectMs"`
	Done           bool     `json:"done,omitempty"`
}

// Server serves the embedded chat UI and the /ws websocket endpoint. It keeps a
// single agent (one conversation shared by all connected tabs).
type Server struct {
	agent  *agent.Agent
	agents *agents.Manager
	cfg    *config.Config
	log    *logger.Logger

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[*session]struct{}
	done     chan struct{} // closed when no clients are connected (current epoch)
	pending  map[string]chan wsMessage
	nextID   int

	processing int32 // shared gate: one message processed at a time
}

// New builds a chat server and redirects the agent's interactive prompts
// (permission questions and the "question" tool) through the websocket.
func New(a *agent.Agent, agentsMgr *agents.Manager, cfg *config.Config, log *logger.Logger) *Server {
	s := &Server{
		agent:    a,
		agents:   agentsMgr,
		cfg:      cfg,
		log:      log,
		sessions: make(map[*session]struct{}),
		pending:  make(map[string]chan wsMessage),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     checkOrigin,
		},
	}
	a.SetCallbacks(s.pathPermissionCallback, s.questionCallback, a.SwitchAgentCallback)
	log.SetSink(s.emit)
	return s
}

// Run starts the HTTP(S) listener and blocks until the server exits or an
// interrupt signal is received (graceful shutdown).
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Bind, s.cfg.Port)
	id := uuid.New().String()
	s.log.LogStage(id, "Starting web server")

	srv := &http.Server{Addr: addr, Handler: s.handler()}

	errCh := make(chan error, 1)
	go func() {
		if s.cfg.Mode == config.ModeWebSecure {
			s.log.Printf("Listening on https://%s (TLS cert: %s, key: %s)", addr, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
			errCh <- srv.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		} else {
			s.log.Printf("Listening on http://%s", addr)
			errCh <- srv.ListenAndServe()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		id := uuid.New().String()
		s.log.LogStage(id, "Shutting down web server")
		if s.cfg.ShutdownTimeout > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.ShutdownTimeout)*time.Second)
			defer cancel()
			return srv.Shutdown(ctx)
		}
		return srv.Shutdown(context.Background())
	}
}

// Handler returns the fully wired HTTP handler (used by tests too).
func (s *Server) Handler() http.Handler {
	return s.handler()
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/ws", s.wsHandler)
	return s.securityHeaders(mux)
}

// securityHeaders sets sane HTTP security headers for every response.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := embedded.ReadFile("index.html")
	if err != nil {
		http.Error(w, "index.html not found in binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// checkOrigin prevents cross-site WebSocket hijacking: a browser origin is only
// accepted when its host matches the request host.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		id := uuid.New().String()
		s.log.LogError(id, fmt.Errorf("websocket upgrade: %w", err))
		return
	}

	sess := &session{server: s, conn: conn}
	s.register(sess)
	defer func() {
		s.unregister(sess)
		sess.close()
	}()

	id := uuid.New().String()
	s.log.LogStage(id, "Web client connected")
	sess.send(wsMessage{Type: typeSystem, Text: "Connected to AI Agent."})
	sess.send(s.statsMsg())
	sess.readLoop()
}

// ---- session set management ----

func (s *Server) register(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		s.done = make(chan struct{})
	}
	s.sessions[sess] = struct{}{}
}

func (s *Server) unregister(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sess)
	if len(s.sessions) == 0 && s.done != nil {
		close(s.done)
		s.done = nil
	}
}

func (s *Server) broadcast(msg wsMessage) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.send(msg)
	}
}

// newPending registers a callback ID and returns channels used to wait for the
// answer. `done` is closed when there are no connected tabs at all.
func (s *Server) newPending() (string, chan wsMessage, chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		return "", nil, nil, errors.New("no connected clients")
	}
	s.nextID++
	id := fmt.Sprintf("p%d", s.nextID)
	ch := make(chan wsMessage, 1)
	s.pending[id] = ch
	return id, ch, s.done, nil
}

// resolve routes an incoming answer/permission to the callback waiting for it.
func (s *Server) resolve(id string, msg wsMessage) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if ok {
		ch <- msg
	}
}

// ---- interactive callbacks (question tool + path permissions) ----

func (s *Server) questionCallback(question string, options []string) (string, error) {
	id, ch, done, err := s.newPending()
	if err != nil {
		return "", err
	}
	s.broadcast(wsMessage{Type: typeQuestion, ID: id, Question: question, Options: options})
	// A nil channel blocks forever in select, so PermissionTimeout == 0 waits
	// indefinitely for the answer.
	var timeoutCh <-chan time.Time
	if s.cfg.PermissionTimeout > 0 {
		timeoutCh = time.After(time.Duration(s.cfg.PermissionTimeout) * time.Second)
	}
	select {
	case msg := <-ch:
		return msg.Text, nil
	case <-done:
		return "", errors.New("no connected clients")
	case <-timeoutCh:
		return "", errors.New("question prompt timed out")
	}
}

func (s *Server) pathPermissionCallback(path string) (tools.PermissionResult, error) {
	return s.askPermission("path", path)
}

func (s *Server) askPermission(kind, target string) (tools.PermissionResult, error) {
	id, ch, done, err := s.newPending()
	if err != nil {
		return tools.PermissionDeny, err
	}
	s.broadcast(wsMessage{Type: typeAskPerm, ID: id, Kind: kind, Target: target})
	// A nil channel blocks forever in select, so PermissionTimeout == 0 waits
	// indefinitely for the user decision.
	var timeoutCh <-chan time.Time
	if s.cfg.PermissionTimeout > 0 {
		timeoutCh = time.After(time.Duration(s.cfg.PermissionTimeout) * time.Second)
	}
	select {
	case msg := <-ch:
		switch strings.ToLower(strings.TrimSpace(msg.Decision)) {
		case "allow", "y":
			return tools.PermissionAllow, nil
		case "allow_all", "a":
			s.agent.HandleWebPermission(kind, target, tools.PermissionAllowAll)
			return tools.PermissionAllowAll, nil
		case "deny_all", "d":
			s.agent.HandleWebPermission(kind, target, tools.PermissionDenyAll)
			return tools.PermissionDenyAll, nil
		default:
			return tools.PermissionDeny, nil
		}
	case <-done:
		return tools.PermissionDeny, errors.New("no connected clients")
	case <-timeoutCh:
		return tools.PermissionDeny, nil
	}
}

// statsMsg builds the status snapshot (model, endpoint, token usage) that the
// header of the chat UI keeps on screen.
func (s *Server) statsMsg() wsMessage {
	prompt, completion, total := s.agent.TokenUsage()
	return wsMessage{
		Type:           typeStats,
		Model:          s.agent.GetModel(),
		API:            s.agent.GetBaseURL(),
		Output:         s.agent.GetOutputFolder(),
		Messages:       s.agent.MessageCount(),
		TokensPrompt:   prompt,
		TokensComplete: completion,
		TokensTotal:    total,
		TokensMax:      s.cfg.MaxTokens,
		StopFallbackMs: s.cfg.StopFallbackMs,
		ReconnectMs:    s.cfg.ReconnectMs,
	}
}

func (s *Server) broadcastStats() {
	s.broadcast(s.statsMsg())
}

// emit relays logger events (responses, tool calls, results, errors) to every
// tab so the UI mirrors the console output. Tool events are split into a title
// (the file path, URL or command the tool operated on) and the payload body, so
// the browser does not have to parse "\n" out of a combined text blob. Every
// event also refreshes the header stats. The third parameter is a unique event
// ID generated by the logger; it is forwarded to clients so they can attach it
// to DOM elements.
func (s *Server) emit(event, text, eventID string) {
	switch event {
	case logger.EventResponse:
		s.broadcast(wsMessage{Type: typeServer, Text: text, ID: eventID, Done: true})
	case logger.EventTool:
		name, target := splitTitle(text)
		if strings.EqualFold(name, "question") {
			return
		}
		s.broadcast(wsMessage{Type: typeSystem, Event: event, Title: toolHeader(name, target), Text: target, ID: eventID})
	case logger.EventToolRes:
		name, result := splitTitle(text)
		if strings.EqualFold(name, "question") {
			return
		}
		s.broadcast(wsMessage{Type: typeSystem, Event: event, Title: name, Text: truncate(result, 4000), ID: eventID})
	case logger.EventError:
		s.broadcast(wsMessage{Type: typeSystem, Event: event, Text: text, ID: eventID})
	case logger.EventDone:
		s.broadcast(wsMessage{Type: typeServerDone, ID: eventID})
	}
	s.broadcastStats()
}

// splitTitle separates the first line of a logger payload from the body.
func splitTitle(text string) (title, content string) {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i], text[i+1:]
	}
	return text, ""
}

// toolHeader builds the bubble title for a tool call: "toolName : target"
// (e.g. "edit : src/main.go", "bash : go build ./..."). The target is the
// "log string" the tools package already extracts from the tool arguments (the
// file path, URL or command), so no JSON parsing is needed here.
func toolHeader(name, target string) string {
	if target == "" {
		return name
	}
	if i := strings.IndexByte(target, '\n'); i >= 0 {
		target = target[:i]
	}
	return name + " : " + target
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "\n...(truncated)"
	}
	return s
}

// handleChat processes a user message from any tab. Processing is serialized so
// the shared conversation stays ordered.
func (s *Server) handleChat(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.processing, 0, 1) {
		s.broadcast(wsMessage{Type: typeSystem, Text: "Please wait, still processing the previous message."})
		return
	}
	defer atomic.StoreInt32(&s.processing, 0)

	stageID := uuid.New().String()
	s.log.LogStage(stageID, "Web user input: "+text)
	s.broadcast(wsMessage{Type: typeUser, Text: text})

	if strings.HasPrefix(text, "/") {
		s.handleCommand(text)
		return
	}

	s.broadcast(wsMessage{Type: typeSystem, Text: "thinking..."})
	if err := s.agent.ProcessMessage(text); err != nil {
		s.broadcast(wsMessage{Type: typeSystem, Text: "Error: " + err.Error()})
	}
	s.broadcast(wsMessage{Type: typeServerDone})
}

func (s *Server) handleCommand(cmd string) {
	parsed, _ := commands.Parse(cmd)
	switch parsed.Action {
	case commands.ActionQuit:
		// Closing the tab is the natural "quit"; keep the server running for
		// other tabs.
		s.broadcast(wsMessage{Type: typeSystem, Text: "Note: /quit disconnects a tab in the browser. The session stays alive for other tabs."})
	case commands.ActionClear:
		s.agent.ClearHistory()
		s.broadcast(wsMessage{Type: typeSystem, Text: "Chat history cleared."})
	case commands.ActionCompact:
		if err := s.agent.CompactSession(); err != nil {
			s.broadcast(wsMessage{Type: typeSystem, Text: "Compact error: " + err.Error()})
		} else {
			s.broadcast(wsMessage{Type: typeSystem, Text: "Session compacted."})
		}
	case commands.ActionTools:
		var b strings.Builder
		for _, t := range s.agent.ListTools() {
			b.WriteString("- " + t.Name + ": " + t.Description + "\n")
		}
		s.broadcast(wsMessage{Type: typeSystem, Text: "Available tools:\n" + b.String()})
	case commands.ActionStatus:
		prompt, completion, total := s.agent.TokenUsage()
		s.broadcast(wsMessage{Type: typeSystem, Text: fmt.Sprintf(
			"Model: %s\nAPI: %s\nOutput: %s\nMessages: %d\nTokens: %d (prompt %d, completion %d)",
			s.agent.GetModel(), s.agent.GetBaseURL(), s.agent.GetOutputFolder(),
			s.agent.MessageCount(), total, prompt, completion)})
	case commands.ActionAgents:
		s.broadcast(wsMessage{Type: typeSystem, Text: "Agents: " + strings.Join(s.agents.List(), ", ")})
	case commands.ActionSwitch:
		if parsed.Arg == "" {
			s.broadcast(wsMessage{Type: typeSystem, Text: "Usage: /switch <agent>. Available: " + strings.Join(s.agents.List(), ", ")})
			return
		}
		if _, err := s.agent.SwitchAgentCallback(parsed.Arg); err != nil {
			s.broadcast(wsMessage{Type: typeSystem, Text: "Switch error: " + err.Error()})
		} else {
			s.broadcast(wsMessage{Type: typeSystem, Text: "Switched to agent: " + parsed.Arg})
		}
	case commands.ActionHelp:
		s.broadcast(wsMessage{Type: typeSystem, Text: commands.HelpText})
	default:
		s.broadcast(wsMessage{Type: typeSystem, Text: "Unknown command: /" + parsed.Name + ". Type /help for commands."})
	}
}

// session represents a single connected tab.
type session struct {
	server  *Server
	conn    *websocket.Conn
	writeMu sync.Mutex
	closed  int32
}

func (sess *session) isClosed() bool {
	return atomic.LoadInt32(&sess.closed) == 1
}

func (sess *session) close() {
	if atomic.SwapInt32(&sess.closed, 1) == 1 {
		return
	}
	sess.conn.Close()
}

func (sess *session) send(msg wsMessage) error {
	if sess.isClosed() {
		return errors.New("websocket session closed")
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	if sess.isClosed() {
		return errors.New("websocket session closed")
	}
	return sess.conn.WriteJSON(msg)
}

// readLoop handles incoming frames. Agent processing runs in a separate
// goroutine so the loop can keep forwarding answers to pending prompts.
func (sess *session) readLoop() {
	for {
		var msg wsMessage
		if err := sess.conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case typeAnswer, typePermission:
			sess.server.resolve(msg.ID, msg)
		case typeChat:
			go sess.server.handleChat(msg.Text)
		case typeStop:
			sess.server.agent.Stop()
		}
	}
}
