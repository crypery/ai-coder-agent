package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	l, err := New(path, false, 1024*1024)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()

	l.Printf("hello %d", 42)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello 42") {
		t.Errorf("log content = %q", data)
	}
}

func TestNewDisabledLogFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	l, err := New(path, true, 1024*1024)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	l.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("log file should not be created when logging is disabled")
	}
}

func TestRotationRotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	l, err := New(path, false, 100)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()

	// Write well past the 100-byte rotation threshold.
	for i := 0; i < 50; i++ {
		l.Printf("line-%d with some padding to grow the file", i)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated backup missing: %v", err)
	}
	// The active log continues to be written after rotation.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log missing: %v", err)
	}
}

func TestSetSinkAndEmit(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "app.log"), false, 1024*1024)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()

	var got string
	l.SetSink(func(event, text, id string) { got = text })
	l.emit("response", "id-1", "the payload")
	if got != "the payload" {
		t.Errorf("sink received %q", got)
	}
}

// Tool events must carry the tool name as the first line so the web UI can
// build the "tool : name : target" header without re-parsing the payload.
func TestLogToolCallEmitsToolNameAndArgs(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "app.log"), true, 1024*1024)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()

	var evt, text string
	l.SetSink(func(event, text2, id string) { evt, text = event, text2 })
	l.LogToolCall("id-1", "read", "src/main.go")
	if evt != EventTool {
		t.Fatalf("event = %q, want %q", evt, EventTool)
	}
	if text != "read\nsrc/main.go" {
		t.Errorf("text = %q, want %q", text, "read\nsrc/main.go")
	}
}

func TestLogToolResultEmitsToolNameAndResult(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "app.log"), true, 1024*1024)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer l.Close()

	var evt, text string
	l.SetSink(func(event, text2, id string) { evt, text = event, text2 })
	l.LogToolResult("id-1", "edit", "ok")
	if evt != EventToolRes {
		t.Fatalf("event = %q, want %q", evt, EventToolRes)
	}
	if text != "edit\nok" {
		t.Errorf("text = %q, want %q", text, "edit\nok")
	}
}