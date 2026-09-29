package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
	// MaxRetries is the number of retries performed for transient failures
	// (connection errors, HTTP 429 and 5xx responses).
	MaxRetries int
	// RetryDelay is the base delay; each retry multiplies it (1s, 2s, ...).
	RetryDelay time.Duration
}

func New(baseURL, apiKey, model string, timeoutSeconds int) *Client {
	if timeoutSeconds < 0 {
		timeoutSeconds = 300
	}
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Client:     &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		MaxRetries: 3,
		RetryDelay: time.Second,
	}
}

type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ToolCallFunction   `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ResponseMessage struct {
	Role        string      `json:"role"`
	Content     interface{} `json:"content"`
	ToolCalls   []ToolCall  `json:"tool_calls"`
}

type ChatCompletionResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []Choice         `json:"choices"`
	Usage   Usage            `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCompletionRequest struct {
	Model            string      `json:"model"`
	Messages         []Message   `json:"messages"`
	Tools            []ToolDef   `json:"tools,omitempty"`
	ToolChoice       interface{} `json:"tool_choice,omitempty"`
	MaxTokens        *int        `json:"max_tokens,omitempty"`
	Temperature      *float64    `json:"temperature,omitempty"`
}

type ToolDef struct {
	Type     string      `json:"type"`
	Function map[string]interface{} `json:"function"`
}

func (c *Client) SendMessage(messages []Message, tools []ToolDef, maxTokens int, temperature float64) (*ChatCompletionResponse, error) {
	reqBody := ChatCompletionRequest{
		Model:       c.Model,
		Messages:    messages,
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
	}

	if len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		resp, err := c.doRequest(jsonData)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt >= c.MaxRetries {
			return nil, err
		}
		delay := c.RetryDelay * time.Duration(attempt+1)
		time.Sleep(delay)
	}
	return nil, lastErr
}

// httpError carries the HTTP status of a failed API response.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.status, e.body)
}

// retryableError wraps an error that is safe to retry.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }

func (e *retryableError) Unwrap() error { return e.err }

func retryable(err error) error { return &retryableError{err: err} }

// isRetryable reports whether a request may be retried: connection-level
// failures and HTTP 429/5xx responses. 4xx client errors and response parsing
// failures are not retried.
func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

func (c *Client) doRequest(jsonData []byte) (*ChatCompletionResponse, error) {
	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, retryable(fmt.Errorf("send request: %w", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, retryable(fmt.Errorf("read response: %w", err))
	}

	if resp.StatusCode != http.StatusOK {
		he := &httpError{status: resp.StatusCode, body: string(body)}
		if he.status == http.StatusTooManyRequests || he.status >= http.StatusInternalServerError {
			return nil, retryable(he)
		}
		return nil, he
	}

	var apiResp ChatCompletionResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	return &apiResp, nil
}
