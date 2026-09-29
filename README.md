# AI Coder Agent

> [Russian version](README_RU.md)

Experimental console-based agent for interacting with LLMs via OpenAI-compatible API.

## Features

- Interactive terminal chat and web interface (HTTP/HTTPS with WebSocket)
- 12 tools: write, read, bash, list, edit, grep, glob, patch, webfetch, websearch, question, todowrite, switch_agent
- Agent switching: build, plan, general, explore, scout, code-reviewer
- Automatic session compaction at 95% token limit
- Full request/response/tool logging with rotation
- Environment variables injection into system prompt

## Architecture

```
main.go
├── config/      — Config loading and validation
├── agents/      — Agent manager (build, plan, general, explore, scout, code-reviewer)
├── agent/       — Main agent loop (ProcessMessage, runLoop, CompactSession)
├── api/         — HTTP client for OpenAI-compatible API
├── context/     — System prompt builder (Environment, Tools, Session, Shell Environment)
├── env/         — Environment collection (directory, cwd, file tree, env vars)
├── session/     — Session management (ID, parent, agent, mode)
├── tools/       — Tool registry and execution
├── commands/    — Command parser (/switch, /clear, ...) for chat and web
├── chat/        — Interactive terminal chat
├── web/         — Web server with WebSocket
└── logger/      — Logging
```

## Configuration

File: `ai-coder-agent.json` (auto-created on first run, never overwritten).

```json
{
  "api_key": "your-api-key",
  "base_url": "https://api.example.com/v1",
  "model": "gpt-4o",
  "output_folder": "./output",
  "log_file": "ai-coder-agent.log",
  "disable_log_file": false,
  "log_rotation_mb": 10,
  "max_tokens": 131072,
  "temperature": 0.2,
  "websearch_limit": 1,
  "webfetch_limit": 3,
  "auto_compact": true,
  "compress_web_text": true,
  "include_env_info": true,
  "max_file_tree": 100,
  "env_var_keys": ["PATH", "HOME", "USER"],
  "mode": "console",
  "port": 8080,
  "bind_address": "127.0.0.1",
  "tls_cert_file": "",
  "tls_key_file": "",
  "bash_timeout": 120,
  "api_timeout": 300,
  "web_client_timeout": 15,
  "shutdown_timeout": 5,
  "stop_fallback_ms": 4000,
  "reconnect_ms": 3000,
  "permission_timeout": 120
}
```

| Parameter | Default | Description |
|-----------|---------|-------------|
| `api_key` | `"apikey"` | Provider API key |
| `base_url` | `"https://127.0.0.1/v1"` | OpenAI-compatible API base URL |
| `model` | `"Qwen3.5-9B-Fable"` | Model to use |
| `output_folder` | `"./output"` | Agent output directory |
| `max_tokens` | `131072` | Maximum tokens |
| `temperature` | `0.2` | Generation temperature |
| `websearch_limit` | `1` | Max websearch calls per request |
| `webfetch_limit` | `3` | Max webfetch calls per request |
| `auto_compact` | `true` | Enable auto-compaction |
| `include_env_info` | `true` | Include ## Environment in prompt |
| `max_file_tree` | `100` | Max files in file tree |
| `env_var_keys` | `["PATH", "HOME", "USER"]` | Env vars for ## Shell Environment |
| `mode` | `"console"` | Mode: console, http, https |
| `port` | `8080` | Web mode port |
| `disable_log_file` | `false` | Disable log file output |
| `log_rotation_mb` | `10` | Log rotation threshold (MB) |
| `bash_timeout` | `120` | Shell command timeout (seconds) |
| `api_timeout` | `300` | LLM HTTP request timeout (seconds) |
| `web_client_timeout` | `15` | webfetch/websearch timeout (seconds) |
| `shutdown_timeout` | `5` | Graceful shutdown period (seconds) |
| `stop_fallback_ms` | `4000` | Browser wait after Stop before UI reset (ms) |
| `reconnect_ms` | `3000` | WebSocket reconnect delay (ms) |
| `permission_timeout` | `120` | Web-UI permission/question timeout (seconds) |

## Agents

| Agent | Mode | Description |
|-------|------|-------------|
| **build** | primary | Default agent, full tool access |
| **plan** | primary | Analysis only, edit=deny, bash=ask |
| **general** | subagent | Multi-step tasks, full access |
| **explore** | subagent | Read-only codebase search |
| **scout** | subagent | Read-only external docs research |
| **code-reviewer** | subagent | Code review, best practices |

Switch via `/switch <agent_name>` or `switch_agent` tool.

## Tools

| Tool | Description | Permission |
|------|-------------|------------|
| `write` | Create/overwrite files | path check |
| `read` | Read files | path check |
| `bash` | Execute shell commands | path check |
| `list` | List files/directories | path check |
| `edit` | Precise text replacement | path check |
| `grep` | Regex search | path check |
| `glob` | Glob pattern search | — |
| `patch` | Apply patches | path check |
| `webfetch` | Fetch URL content | limit check |
| `websearch` | DuckDuckGo search | limit check |
| `question` | Ask user | callback |
| `todowrite` | Update todo list | — |
| `switch_agent` | Switch agent | callback |

### Permissions

- File: `permission.txt` in working directory
- Format: `allowed_paths|/path` and `denied_paths|/path`
- Paths inside `output_folder` are allowed; others require approval
- Destructive commands (`shutdown*`, `rm*`, `del*`) are simulated, not executed

## Modes

- **Console** — interactive terminal chat with commands: `/quit`, `/clear`, `/compact`, `/tools`, `/status`, `/agents`, `/switch`, `/help`
- **HTTP/HTTPS** — web server with WebSocket, endpoints: `/chat`, `/ws`

## Testing

```bash
go test ./...
go test -race ./...
```

## Known Limitations

- No AGENTS.md support
- No VCS info (git branch/status)
- No max steps limit
- DuckDuckGo HTML parsing is fragile
- Patch requires exact context match (no fuzz)

## License

GNU AGPL v3
