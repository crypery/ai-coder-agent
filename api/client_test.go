package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendMessageOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret", "m", 5)
	resp, err := c.SendMessage([]Message{{Role: "user", Content: "hello"}}, nil, 100, 0.2)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.Choices[0].Message.Content != "hi" {
		t.Errorf("content = %v", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Errorf("total tokens = %d", resp.Usage.TotalTokens)
	}
}

func TestSendMessageSendsTools(t *testing.T) {
	var gotBody ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", "m", 5)
	tools := []ToolDef{{Type: "function", Function: map[string]interface{}{"name": "read"}}}
	if _, err := c.SendMessage([]Message{{Role: "user", Content: "x"}}, tools, 100, 0.2); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(gotBody.Tools) != 1 {
		t.Errorf("tools = %d, want 1", len(gotBody.Tools))
	}
	if gotBody.ToolChoice != "auto" {
		t.Errorf("tool_choice = %v, want auto", gotBody.ToolChoice)
	}
	if gotBody.Model != "m" {
		t.Errorf("model = %q", gotBody.Model)
	}
}

func TestSendMessageNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", "m", 5)
	if _, err := c.SendMessage([]Message{{Role: "user", Content: "x"}}, nil, 100, 0.2); err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestSendMessageServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := New(url, "k", "m", 5)
	c.MaxRetries = 0
	if _, err := c.SendMessage([]Message{{Role: "user", Content: "x"}}, nil, 100, 0.2); err == nil {
		t.Fatal("expected error for down server")
	}
}

func TestSendMessageRetriesOn5xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"retried"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", "m", 5)
	c.MaxRetries = 1
	c.RetryDelay = time.Millisecond
	resp, err := c.SendMessage([]Message{{Role: "user", Content: "x"}}, nil, 100, 0.2)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.Choices[0].Message.Content != "retried" {
		t.Errorf("content = %v", resp.Choices[0].Message.Content)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 calls, got %d", calls)
	}
}