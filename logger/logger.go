package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

type Logger struct {
	fileLogger     *log.Logger
	console        *log.Logger
	file           io.WriteCloser
	disableLogFile bool
	sink           func(event, text, id string)
	silentActivity bool
}

// Event names emitted to an optional sink.
const (
	EventResponse = "response"
	EventTool     = "tool"
	EventToolRes  = "tool_result"
	EventError    = "error"
	EventStage    = "stage"
	EventAgentCall    = "agent_call"
	EventAgentResult  = "agent_result"
	EventDone         = "done"
)

// SetSink registers a callback that receives structured events (responses,
// tool calls, errors, stages) so alternate frontends (e.g. a websocket UI) can
// mirror what the console shows. Events are emitted synchronously. The second
// argument is a unique identifier generated for each event call; callers can use
// it to correlate events or attach them to DOM elements.
func (l *Logger) SetSink(s func(event, text, id string)) {
	l.sink = s
}

// SetSilentActivity stops echoing chat activity (requests, responses, tool
// calls, errors) to the console (stdout). The log file and the sink still
// receive everything. The web server enables this so the terminal stays quiet
// while the browser is the frontend.
func (l *Logger) SetSilentActivity(silent bool) {
	l.silentActivity = silent
}

func (l *Logger) emit(event, id string, text string) string {
	if l.sink != nil {
		l.sink(event, text, id)
	}
	return id
}

// rotationWriter is a file-backed writer that rotates the log to
// "<path>.1" once it grows past maxBytes.
type rotationWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	size     int64
}

func newRotationWriter(path string, maxBytes int64) (*rotationWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	size := int64(0)
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	return &rotationWriter{path: path, maxBytes: maxBytes, file: f, size: size}, nil
}

func (w *rotationWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate moves the current log to "<path>.1" (replacing any previous backup)
// and starts a fresh file.
func (w *rotationWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	backup := w.path + ".1"
	_ = os.Remove(backup)
	if err := os.Rename(w.path, backup); err != nil {
		f, oerr := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if oerr != nil {
			return oerr
		}
		w.file = f
		w.size = 0
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}

func (w *rotationWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func New(logFile string, disableLogFile bool, maxBytes int64) (*Logger, error) {
	var fileOut io.WriteCloser
	if !disableLogFile && logFile != "" {
		rw, err := newRotationWriter(logFile, maxBytes)
		if err != nil {
			return nil, err
		}
		fileOut = rw
	}

	var multi io.Writer = os.Stdout
	if fileOut != nil {
		multi = io.MultiWriter(os.Stdout, fileOut)
	}

	var fileLogger io.Writer = io.Discard
	if fileOut != nil {
		fileLogger = fileOut
	}

	return &Logger{
		fileLogger:     log.New(fileLogger, "", log.LstdFlags),
		console:        log.New(multi, "", 0),
		file:           fileOut,
		disableLogFile: disableLogFile,
	}, nil
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

func (l *Logger) Print(v ...interface{}) {
	l.fileLogger.Println(v...)
	l.console.Println(v...)
}

func (l *Logger) Printf(format string, v ...interface{}) {
	l.fileLogger.Printf(format, v...)
	l.console.Printf(format, v...)
}

// PrintfActivity writes chat activity (e.g. per-request token summaries) to the
// log file, and to the console only while chat activity is still echoed.
func (l *Logger) PrintfActivity(format string, v ...interface{}) {
	l.fileLogger.Printf(format, v...)
	if !l.silentActivity {
		l.console.Printf(format, v...)
	}
}

func (l *Logger) LogRequest(id, systemPrompt, userMessage string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== request ==========", t)
	l.fileLogger.Printf("[REQ] System: %s", systemPrompt)
	l.fileLogger.Printf("[REQ] User: %s", userMessage)
	if !l.silentActivity {
		l.console.Printf("thinking...\n")
	}
}

func (l *Logger) LogResponse(id, content string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== response ==========", t)
	l.fileLogger.Printf("[RES] %s", content)
	if !l.silentActivity {
		l.console.Printf("\n========== response ==========\n")
		l.console.Printf("%s\n", content)
	}
	l.emit(EventResponse, id, content)
}

func (l *Logger) LogToolCall(id, toolName string, args string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== %s ==========", t, toolName)
	l.fileLogger.Printf("[TOOL] %s", args)
	if (!l.silentActivity) {
		l.console.Printf("\n========== %s ==========\n", toolName)
		l.console.Printf("%s\n", args)
	}
	l.emit(EventTool, id, toolName+"\n"+args)
}

func (l *Logger) LogToolResult(id, toolName string, result string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== %s ==========", t, toolName)
	l.fileLogger.Printf("[RESULT] %s", result)
	switch toolName {
		case "read", "webfetch", "websearch":
		return
	}
	if !l.silentActivity {
		l.console.Printf("\n========== %s ==========\n", toolName)
		l.console.Printf("%s\n", result)
	}
	l.emit(EventToolRes, id, toolName+"\n"+result)
}

func (l *Logger) LogStage(id, stage string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== stage ==========", t)
	l.fileLogger.Printf("[STAGE] %s", stage)
	//l.console.Printf("\n========== stage ==========\n")
	//l.console.Printf("%s\n", stage)
	l.emit(EventStage, id, stage)
}

func (l *Logger) LogError(id string, err error) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== error ==========", t)
	l.fileLogger.Printf("[ERROR] %v", err)
	if !l.silentActivity {
		l.console.Printf("\n========== error ==========\n")
		l.console.Printf("%v\n", err)
	}
	l.emit(EventError, id, err.Error())
}

func (l *Logger) LogAgentCall(id, agentName, model string, messageCount, toolCount int) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== agent_call ==========", t)
	l.fileLogger.Printf("[AGENT] agent=%s model=%s messages=%d tools=%d", agentName, model, messageCount, toolCount)
	if !l.silentActivity {
		l.console.Printf("[AGENT CALL] agent=%s model=%s messages=%d tools=%d\n", agentName, model, messageCount, toolCount)
	}
	l.emit(EventAgentCall, id, fmt.Sprintf("agent=%s model=%s messages=%d tools=%d", agentName, model, messageCount, toolCount))
}

func (l *Logger) LogDone(id string) {
	l.emit(EventDone, id, "request done")
}

func (l *Logger) LogAgentResult(id, agentName, model string, responseModel string, choiceCount, promptTokens, completionTokens, totalTokens int, finishReason string) {
	t := time.Now().Format("15:04:05")
	l.fileLogger.Printf("[%s] ========== agent_result ==========", t)
	l.fileLogger.Printf("[AGENT] agent=%s model=%s response_model=%s choices=%d prompt_tokens=%d completion_tokens=%d total_tokens=%d finish_reason=%s",
		agentName, model, responseModel, choiceCount, promptTokens, completionTokens, totalTokens, finishReason)
	if !l.silentActivity {
		l.console.Printf("[AGENT RESULT] agent=%s model=%s tokens=%d/%d/%d finish=%s\n",
			agentName, model, promptTokens, completionTokens, totalTokens, finishReason)
	}
	l.emit(EventAgentResult, id, fmt.Sprintf("agent=%s model=%s tokens=%d/%d/%d finish=%s", agentName, model, promptTokens, completionTokens, totalTokens, finishReason))
}
