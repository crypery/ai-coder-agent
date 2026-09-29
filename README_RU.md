# AI Coder Agent

> [English version](README.md)

Экспериментальный консольный агент для взаимодействия с LLM через OpenAI-compatible API.

## Возможности

- Интерактивный терминальный чат и веб-интерфейс (HTTP/HTTPS с WebSocket)
- 12 инструментов: write, read, bash, list, edit, grep, glob, patch, webfetch, websearch, question, todowrite, switch_agent
- Переключение агентов: build, plan, general, explore, scout, code-reviewer
- Автоматическая компактизация сессии при достижении 95% лимита токенов
- Полное логирование запросов/ответов/инструментов с ротацией
- Передача переменных окружения в системный промпт

## Архитектура

```
main.go
├── config/      — Загрузка и валидация конфигурации
├── agents/      — Менеджер агентов (build, plan, general, explore, scout, code-reviewer)
├── agent/       — Основной цикл агента (ProcessMessage, runLoop, CompactSession)
├── api/         — HTTP-клиент для OpenAI-compatible API
├── context/     — Билдер системного промпта (Environment, Tools, Session, Shell Environment)
├── env/         — Сбор окружения (directory, cwd, file tree, env vars)
├── session/     — Управление сессиями (ID, parent, agent, mode)
├── tools/       — Реестр инструментов и их выполнение
├── commands/    — Парсер команд (/switch, /clear, ...) для chat и web
├── chat/        — Интерактивный терминальный чат
├── web/         — Веб-сервер с WebSocket
└── logger/      — Логирование
```

## Конфигурация

Файл: `ai-coder-agent.json` (создаётся автоматически при первом запуске, никогда не перезаписывается).

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

| Параметр | По умолчанию | Описание |
|----------|--------------|----------|
| `api_key` | `"apikey"` | API ключ провайдера |
| `base_url` | `"https://127.0.0.1/v1"` | Base URL OpenAI-compatible API |
| `model` | `"Qwen3.5-9B-Fable"` | Модель |
| `output_folder` | `"./output"` | Директория для файлов агента |
| `max_tokens` | `131072` | Максимум токенов |
| `temperature` | `0.2` | Температура генерации |
| `websearch_limit` | `1` | Максимум websearch на запрос |
| `webfetch_limit` | `3` | Максимум webfetch на запрос |
| `auto_compact` | `true` | Автокомпактизация |
| `include_env_info` | `true` | Включать ## Environment в промпт |
| `max_file_tree` | `100` | Максимум файлов в file tree |
| `env_var_keys` | `["PATH", "HOME", "USER"]` | Переменные окружения для ## Shell Environment |
| `mode` | `"console"` | Режим: console, http, https |
| `port` | `8080` | Порт для web-режима |
| `disable_log_file` | `false` | Отключить запись в файл лога |
| `log_rotation_mb` | `10` | Ротация лога при превышении размера (МБ) |
| `bash_timeout` | `120` | Таймаут shell-команды (секунды) |
| `api_timeout` | `300` | Таймаут HTTP-запроса к LLM (секунды) |
| `web_client_timeout` | `15` | Таймаут webfetch/websearch (секунды) |
| `shutdown_timeout` | `5` | Graceful shutdown веб-сервера (секунды) |
| `stop_fallback_ms` | `4000` | Ожидание браузера после Stop до сброса UI (мс) |
| `reconnect_ms` | `3000` | Задержка переподключения WebSocket (мс) |
| `permission_timeout` | `120` | Таймаут ожидания ответа на permission/question в web-UI (секунды) |

## Агенты

| Агент | Режим | Описание |
|-------|-------|----------|
| **build** | primary | Агент по умолчанию, полный доступ к инструментам |
| **plan** | primary | Только анализ, edit=deny, bash=ask |
| **general** | subagent | Многоэтапные задачи, полный доступ |
| **explore** | subagent | Read-only поиск по кодовой базе |
| **scout** | subagent | Read-only исследование внешней документации |
| **code-reviewer** | subagent | Review кода, best practices |

Переключение: `/switch <agent_name>` или инструмент `switch_agent`.

## Инструменты

| Инструмент | Описание | Permission |
|------------|----------|------------|
| `write` | Создание/перезапись файлов | path check |
| `read` | Чтение файлов | path check |
| `bash` | Выполнение shell-команд | path check |
| `list` | Список файлов/директорий | path check |
| `edit` | Точечная замена текста | path check |
| `grep` | Поиск по regex | path check |
| `glob` | Поиск по glob-паттерну | — |
| `patch` | Применение патчей | path check |
| `webfetch` | Получение контента по URL | limit check |
| `websearch` | Поиск в DuckDuckGo | limit check |
| `question` | Вопрос пользователю | callback |
| `todowrite` | Обновление todo-листа | — |
| `switch_agent` | Переключение агента | callback |

### Разрешения

- Файл: `permission.txt` в рабочей директории
- Формат: `allowed_paths|/path` и `denied_paths|/path`
- Пути внутри `output_folder` разрешены; остальные требуют подтверждения
- Деструктивные команды (`shutdown*`, `rm*`, `del*`) имитируются, не выполняются

## Режимы работы

- **Console** — интерактивный терминальный чат, команды: `/quit`, `/clear`, `/compact`, `/tools`, `/status`, `/agents`, `/switch`, `/help`
- **HTTP/HTTPS** — веб-сервер с WebSocket, эндпоинты: `/chat`, `/ws`

## Тестирование

```bash
go test ./...
go test -race ./...
```

## Известные ограничения

- Нет поддержки AGENTS.md
- Нет информации о VCS (git branch/status)
- Нет лимита шагов
- DuckDuckGo HTML parsing нестабилен
- Patch требует точного совпадения контекста (без fuzz)

## Лицензия

GNU AGPL v3
