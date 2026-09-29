/*
 * Git: https://github.com/crypery
 * Author: https://crypery.com
 * License: GNU AGPL v3 (Affero GPL)
 */

// AI Coder Agent Console — a multi-functional console agent for interacting with
// AI through an OpenAI-compatible HTTP/HTTPS API. The agent provides an
// interactive terminal chat with message history, a tool system (write, read,
// bash, list, edit, grep, glob, patch, webfetch, websearch, question,
// todowrite) for working with the filesystem and running shell commands, as
// well as full logging of all requests, responses and tool calls to a file.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"ai-coder-agent/agent"
	"ai-coder-agent/agents"
	"ai-coder-agent/chat"
	"ai-coder-agent/config"
	"ai-coder-agent/logger"
	"ai-coder-agent/web"

	"github.com/google/uuid"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.LogFile, cfg.DisableLogFile, int64(cfg.LogRotationMB)*1024*1024)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	id := uuid.New().String()
	log.LogStage(id, "AI Agent Starting")
	log.Printf("Configuration loaded successfully")

	// In web mode the browser is the frontend, so chat activity (requests,
	// responses, tool calls) must not be echoed to the terminal; it still goes
	// to the log file and to the connected web clients.
	if cfg.Mode == config.ModeWeb || cfg.Mode == config.ModeWebSecure {
		log.SetSilentActivity(true)
	}

	if err := os.MkdirAll(cfg.OutputFolder, 0755); err != nil {
		id := uuid.New().String()
		log.LogError(id, fmt.Errorf("create output folder: %w", err))
		os.Exit(1)
	}

	agentsMgr := agents.NewManager()
	agentsMgr.LoadBuiltIn()

	a, err := agent.New(cfg, log, agentsMgr)
	if err != nil {
		id := uuid.New().String()
		log.LogError(id, fmt.Errorf("create agent: %w", err))
		os.Exit(1)
	}

	id = uuid.New().String()
	log.LogStage(id, "Agent initialized")

	id = uuid.New().String()
	log.LogStage(id, "Connecting to AI API")
	if err := a.VerifyConnection(); err != nil {
		id := uuid.New().String()
		log.LogError(id, fmt.Errorf("API connection failed: %w", err))
		log.Printf("Waiting for API connection at %s...", cfg.BaseURL)
		retry := 0
		for {
			retry++
			if retry > 30 {
				id := uuid.New().String()
				log.LogError(id, fmt.Errorf("max retries reached, cannot connect to API"))
				os.Exit(1)
			}
			log.Printf("  Retry %d/30...", retry)
			if err := a.VerifyConnection(); err == nil {
				break
			}
			log.Printf("  Connection failed, retrying in 5s...")
			time.Sleep(5 * time.Second)
		}
		id = uuid.New().String()
		log.LogStage(id, "Connected to AI API successfully")
		log.Printf("Connected to %s (model: %s)", cfg.BaseURL, cfg.Model)
	} else {
		id = uuid.New().String()
		log.LogStage(id, "Connected to AI API successfully")
		log.Printf("Connected to %s (model: %s)", cfg.BaseURL, cfg.Model)
	}

	id = uuid.New().String()
	log.LogStage(id, "Agent ready, starting chat")

	switch cfg.Mode {
	case config.ModeConsole:
		c := chat.New(a, log, agentsMgr)
		c.Run()
	case config.ModeWeb, config.ModeWebSecure:
		srv := web.New(a, agentsMgr, cfg, log)
		if err := srv.Run(); err != nil {
			id := uuid.New().String()
			log.LogError(id, fmt.Errorf("web server: %w", err))
			log.Printf("Failed to start web server. Check mode, port and TLS settings in ai-coder-agent.json")
			os.Exit(1)
		}
	}

	id = uuid.New().String()
	log.LogStage(id, "AI Agent Shutting Down")
}

func loadConfig() (*config.Config, error) {
	cfg := config.Default()

	configPaths := []string{
		"ai-coder-agent.json",
	}

	configFound := false
	for _, path := range configPaths {
		if _, err := os.Stat(path); err == nil {
			loadedCfg, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			cfg = loadedCfg
			configFound = true
			break
		}
	}

	// Only write a fresh default file when none exists yet. Never overwrite an
	// existing config even if it has an empty api_key (the user may intend to
	// supply the key some other way).
	if !configFound {
		jsonData, err := json.MarshalIndent(cfg, "", "  ")
		if err == nil {
			if err := os.WriteFile("ai-coder-agent.json", jsonData, 0644); err == nil {
				fmt.Fprintf(os.Stderr, "Created default ai-coder-agent.json\n")
			}
		}
	}

	return cfg, nil
}
