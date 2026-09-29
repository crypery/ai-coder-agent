package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ModeConsole runs the interactive terminal chat.
const ModeConsole = "console"

// ModeWeb binds an HTTP server.
const ModeWeb = "http"

// ModeWebSecure binds an HTTPS server.
const ModeWebSecure = "https"

type Config struct {
	APIKey          string  `json:"api_key"`
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	OutputFolder    string  `json:"output_folder"`
	LogFile         string  `json:"log_file"`
	DisableLogFile  bool    `json:"disable_log_file"`
	LogRotationMB   int     `json:"log_rotation_mb"`
	MaxTokens       int     `json:"max_tokens"`
	Temperature     float64 `json:"temperature"`
	WebSearchLimit  int     `json:"websearch_limit"`
	WebFetchLimit   int     `json:"webfetch_limit"`
	AutoCompact     bool    `json:"auto_compact"`
	CompressWebText bool    `json:"compress_web_text"`

	// IncludeEnvInfo controls whether ## Environment section is injected into
	// the system prompt on the first request.
	IncludeEnvInfo bool `json:"include_env_info"`

	// MaxFileTree limits the number of files listed in the ## Environment file
	// tree. 0 means use the default (100).
	MaxFileTree int `json:"max_file_tree"`

	// EnvVarKeys lists environment variable names to include in the ## Shell
	// Environment section of the system prompt.
	EnvVarKeys []string `json:"env_var_keys"`

	// Mode selects the run mode: "console", "http" or "https".
	Mode string `json:"mode"`
	// Port is the listening port for http/https modes.
	Port int `json:"port"`
	// Bind is the IP address to listen on (0.0.0.0 for all interfaces).
	Bind string `json:"bind_address"`
	// TLSCertFile / TLSKeyFile point to the certificate and private key used
	// when Mode is "https".
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`

	// BashTimeout limits how long a shell command may run (seconds). 0 = no limit.
	BashTimeout int `json:"bash_timeout"`
	// APITimeout limits a single LLM API request (seconds). 0 = no limit.
	APITimeout int `json:"api_timeout"`
	// WebClientTimeout limits webfetch/websearch HTTP requests (seconds). 0 = no limit.
	WebClientTimeout int `json:"web_client_timeout"`
	// ShutdownTimeout is the graceful shutdown grace period for the web server
	// (seconds). 0 = wait indefinitely.
	ShutdownTimeout int `json:"shutdown_timeout"`
	// StopFallbackMs is how long the browser waits after Stop before force
	// resetting the UI if no server_done arrives (milliseconds). 0 = never
	// force-reset.
	StopFallbackMs int `json:"stop_fallback_ms"`
	// PermissionTimeout is how long the web UI may keep a request waiting for
	// a permission confirmation or question answer before it auto-denies and
	// stops the request (seconds). 0 = wait indefinitely.
	PermissionTimeout int `json:"permission_timeout"`
	// ReconnectMs is the browser websocket reconnect delay (milliseconds).
	ReconnectMs int `json:"reconnect_ms"`
}

func Default() *Config {
	return &Config{
		APIKey:          "apikey",
		BaseURL:         "https://127.0.0.1/v1",
		Model:           "Qwen3.5-9B-Fable",
		OutputFolder:    "output",
		LogFile:         "ai-coder-agent.log",
		DisableLogFile:  false,
		LogRotationMB:   10,
		WebSearchLimit:  1,
		WebFetchLimit:   3,
		MaxTokens:       131072,
		Temperature:     0.2,
		AutoCompact:     true,
		CompressWebText: true,
		IncludeEnvInfo:  true,
		MaxFileTree:     100,
		EnvVarKeys:      []string{"PATH", "HOME", "USER"},
		Mode:            ModeConsole,
		Port:            8080,
		Bind:            "127.0.0.1",
		BashTimeout:     120,
		APITimeout:      300,
		WebClientTimeout: 15,
		ShutdownTimeout: 5,
		StopFallbackMs:  4000,
		ReconnectMs:     3000,
		PermissionTimeout: 120,
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Strip a UTF-8 BOM that some Windows editors write; encoding/json would
	// reject it otherwise.
	data = []byte(strings.TrimPrefix(string(data), "\uFEFF"))

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("api_key is required (set api_key in ai-coder-agent.json)")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}

	if c.MaxTokens <= 0 {
		c.MaxTokens = 4096
	}
	if c.Temperature < 0 || c.Temperature > 2 {
		c.Temperature = 0.2
	}
	if c.WebSearchLimit < 0 {
		c.WebSearchLimit = 1
	}
	if c.WebFetchLimit < 0 {
		c.WebFetchLimit = 3
	}
	if c.LogRotationMB < 0 {
		c.LogRotationMB = 10
	}

	// Timeouts: zero means "no limit". Negative values fall back to the defaults.
	if c.BashTimeout < 0 {
		c.BashTimeout = 120
	}
	if c.APITimeout < 0 {
		c.APITimeout = 300
	}
	if c.WebClientTimeout < 0 {
		c.WebClientTimeout = 15
	}
	if c.ShutdownTimeout < 0 {
		c.ShutdownTimeout = 5
	}
	if c.StopFallbackMs < 0 {
		c.StopFallbackMs = 4000
	}
	if c.ReconnectMs < 0 {
		c.ReconnectMs = 3000
	}
	if c.PermissionTimeout < 0 {
		c.PermissionTimeout = 120
	}

	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = ModeConsole
	}
	switch c.Mode {
	case ModeConsole, ModeWeb, ModeWebSecure:
	default:
		return fmt.Errorf("mode must be one of %q, %q or %q", ModeConsole, ModeWeb, ModeWebSecure)
	}

	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if c.Port == 0 {
		c.Port = 8080
	}

	c.Bind = strings.TrimSpace(c.Bind)
	if c.Bind == "" {
		c.Bind = "127.0.0.1"
	}

	if c.Mode == ModeWebSecure {
		if c.TLSCertFile == "" || c.TLSKeyFile == "" {
			return fmt.Errorf("tls_cert_file and tls_key_file are required when mode is %q", ModeWebSecure)
		}
		if _, err := os.Stat(c.TLSCertFile); err != nil {
			return fmt.Errorf("tls cert file not accessible %q: %w", c.TLSCertFile, err)
		}
		if _, err := os.Stat(c.TLSKeyFile); err != nil {
			return fmt.Errorf("tls key file not accessible %q: %w", c.TLSKeyFile, err)
		}
	}

	return nil
}
