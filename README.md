# resp2chat - Go LLM API Protocol Proxy

A high-performance Go proxy that translates between OpenAI's Responses API and Chat Completions API, enabling Codex and other clients to work with any LLM provider.

## Features

- **Bidirectional Protocol Translation**: Responses API ↔ Chat Completions API
- **Multi-Channel Support**: Configure multiple upstream providers (MiMo, DeepSeek, etc.)
- **Multi-Key Management**: Round-robin, random, least-errors, least-latency strategies
- **Key Health Tracking**: Auto-pause on consecutive errors, auto-resume after cooldown
- **Fan-Out**: Send requests to multiple keys simultaneously, return first success
- **Streaming SSE**: Full streaming support in both directions
- **Embedded Web UI**: Alpine.js dashboard for management
- **Structured Logging**: Configurable log levels and request body capture
- **SQLite Persistence**: Request logs and key statistics survive restarts

## Quick Start

```bash
# 1. Build
cd ai-adapter
go build -o resp2chat ./cmd/server/

# 2. Configure
cp config.example.yaml config.yaml
# Edit config.yaml with your API keys

# 3. Run
./resp2chat -config config.yaml
```

## Configuration

See `config.example.yaml` for full documentation. Key sections:

- `server`: Host, port, API token
- `logging`: Level, file, request body capture
- `channels`: Upstream providers with keys, models, strategies

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /v1/responses` | Responses API (Codex) |
| `POST /v1/chat/completions` | Chat Completions API |
| `GET /` | Admin Web UI |
| `GET /admin/api/stats` | Statistics |
| `GET /admin/api/channels` | Channel list |
| `GET /admin/api/logs` | Request logs |

## Architecture

```
Client (Codex/HTTP) → Proxy → Channel Selection → Key Pool → Upstream LLM
                                    ↓
                              Translation Layer
                         (Responses ↔ Chat Completions)
```

## License

MIT

