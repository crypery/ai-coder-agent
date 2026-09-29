package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Mode != ModeConsole {
		t.Errorf("default mode = %q, want %q", c.Mode, ModeConsole)
	}
	if c.Port != 8080 {
		t.Errorf("default port = %d, want 8080", c.Port)
	}
	if c.Bind != "127.0.0.1" {
		t.Errorf("default bind = %q, want 127.0.0.1", c.Bind)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestValidateMode(t *testing.T) {
	cases := []struct {
		mode    string
		wantErr bool
	}{
		{"console", false},
		{"http", false},
		{"https", true}, // no cert files configured
		{"banana", true},
		{"", false}, // empty falls back to console
	}
	for _, tc := range cases {
		c := Default()
		c.Mode = tc.mode
		err := c.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("mode %q: expected error, got nil", tc.mode)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("mode %q: unexpected error %v", tc.mode, err)
		}
	}
}

func TestValidatePort(t *testing.T) {
	c := Default()
	c.Mode = ModeWeb
	c.Port = 70000
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for port 70000")
	}
	c.Port = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("port 0 should default: %v", err)
	}
	if c.Port != 8080 {
		t.Errorf("port after default = %d, want 8080", c.Port)
	}
}

func TestValidateHTTPSFileRequirements(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}

	c := Default()
	c.Mode = ModeWebSecure
	c.TLSCertFile = cert
	c.TLSKeyFile = key
	if err := c.Validate(); err != nil {
		t.Fatalf("valid https config rejected: %v", err)
	}

	// Missing key file must fail.
	c.TLSKeyFile = filepath.Join(dir, "missing.pem")
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing key file")
	}
	// Empty cert must fail.
	c.TLSKeyFile = key
	c.TLSCertFile = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty cert file")
	}
}

func TestLoadJSONConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
	  "api_key": "k",
	  "mode": "https",
	  "port": 8443,
	  "bind_address": "0.0.0.0",
	  "tls_cert_file": "c.pem",
	  "tls_key_file": "k.pem"
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != ModeWebSecure {
		t.Errorf("mode = %q, want https", cfg.Mode)
	}
	if cfg.Port != 8443 {
		t.Errorf("port = %d, want 8443", cfg.Port)
	}
	if cfg.Bind != "0.0.0.0" {
		t.Errorf("bind = %q, want 0.0.0.0", cfg.Bind)
	}
	if cfg.TLSCertFile != "c.pem" || cfg.TLSKeyFile != "k.pem" {
		t.Errorf("tls files not parsed: %q / %q", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
}

func TestDefaultTimeouts(t *testing.T) {
	c := Default()
	want := map[string]int{
		"BashTimeout":      c.BashTimeout,
		"APITimeout":       c.APITimeout,
		"WebClientTimeout": c.WebClientTimeout,
		"ShutdownTimeout":  c.ShutdownTimeout,
		"StopFallbackMs":   c.StopFallbackMs,
		"ReconnectMs":      c.ReconnectMs,
		"PermissionTimeout": c.PermissionTimeout,
	}
	for name, v := range want {
		if v <= 0 {
			t.Errorf("%s default must be positive, got %d", name, v)
		}
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestValidateFallsBackToDefaultTimeouts(t *testing.T) {
	c := Default()
	c.BashTimeout = -1
	c.APITimeout = -5
	c.WebClientTimeout = -2
	c.ShutdownTimeout = -1
	c.StopFallbackMs = -10
	c.ReconnectMs = -10
	c.PermissionTimeout = -3
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.BashTimeout != 120 || c.APITimeout != 300 || c.WebClientTimeout != 15 {
		t.Errorf("seconds defaults wrong: bash=%d api=%d web=%d", c.BashTimeout, c.APITimeout, c.WebClientTimeout)
	}
	if c.ShutdownTimeout != 5 || c.StopFallbackMs != 4000 || c.ReconnectMs != 3000 {
		t.Errorf("ms defaults wrong: shutdown=%d fallback=%d reconnect=%d", c.ShutdownTimeout, c.StopFallbackMs, c.ReconnectMs)
	}
	if c.PermissionTimeout != 120 {
		t.Errorf("permission timeout default = %d, want 120", c.PermissionTimeout)
	}
}

func TestValidateZeroMeansNoLimit(t *testing.T) {
	c := Default()
	c.BashTimeout = 0
	c.APITimeout = 0
	c.WebClientTimeout = 0
	c.ShutdownTimeout = 0
	c.StopFallbackMs = 0
	c.ReconnectMs = 0
	c.PermissionTimeout = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for name, v := range map[string]int{
		"BashTimeout":      c.BashTimeout,
		"APITimeout":       c.APITimeout,
		"WebClientTimeout": c.WebClientTimeout,
		"ShutdownTimeout":  c.ShutdownTimeout,
		"StopFallbackMs":   c.StopFallbackMs,
		"ReconnectMs":      c.ReconnectMs,
		"PermissionTimeout": c.PermissionTimeout,
	} {
		if v != 0 {
			t.Errorf("%s = %d, want 0 (no limit)", name, v)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected default config")
	}
}

func TestLoadToleratesUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf{\"mode\":\"http\"}"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("BOM-prefixed config should parse: %v", err)
	}
	if cfg.Mode != ModeWeb {
		t.Errorf("mode = %q, want http", cfg.Mode)
	}
}