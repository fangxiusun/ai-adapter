# ai-adapter

A high-performance Go proxy that translates between four major LLM API protocols, enabling any client to work with any LLM provider.

## Supported Protocols

| Protocol | Endpoint | Stream Support |
|----------|----------|---------------|
| OpenAI Chat Completions | `POST /v1/chat/completions` | SSE |
| OpenAI Responses | `POST /v1/responses` | SSE |
| Anthropic Claude Messages | `POST /v1/messages` | SSE |
| Google Gemini generateContent | `POST /v1beta/models/{model}:generateContent` | JSON stream |

All 12 cross-protocol conversion directions are supported, each in both streaming and non-streaming mode.

## Features

- **4-Protocol Interop**: Chat / Responses / Claude Messages / Gemini — any-to-any conversion
- **Interface Capability Model**: Each channel declares native protocol support via URLs; missing protocols are auto-converted
- **Multi-Channel**: Configure multiple upstream providers with independent keys, models, and strategies
- **Multi-Key Management**: Round-robin, random, least-errors, least-latency strategies
- **Key Health Tracking**: Auto-pause on consecutive errors, auto-resume after cooldown
- **Fan-Out**: Concurrent multi-key requests, return first success
- **Streaming SSE**: Event-level streaming conversion for all protocol pairs
- **Embedded Web UI**: Alpine.js management dashboard
- **Structured Logging**: Configurable levels with optional request body capture
- **SQLite Persistence**: Request logs and key statistics survive restarts

## Quick Start

```bash
# 1. Build
cd ai-adapter
go build -o ai-adapter ./cmd/server/
# Or use the build script:
# .\scripts\build.ps1 -Windows

# 2. Configure
cp config.example.yaml config.yaml
# Edit config.yaml with your API keys

# 3. Run
./ai-adapter -config config.yaml
```

## Configuration

Channels declare their capabilities via interface URLs:

```yaml
channels:
  - id: "my-provider"
    enabled: true
    chat_url: "https://api.example.com"         # Supports Chat Completions
    # responses_url: "https://..."               # Supports Responses
    # messages_url: "https://..."                # Supports Claude Messages
    # generate_content_url: "https://..."        # Supports Gemini
    models:
      - id: "gpt-4o"
    default_model: "gpt-4o"
    keys:
      - value: "sk-your-key"
```

See `config.example.yaml` for full documentation.

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic Claude Messages |
| `POST /v1beta/models/{model}:generateContent` | Gemini (non-stream) |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini (stream) |
| `GET /` | Admin Web UI |
| `GET /admin/api/stats` | Statistics |
| `GET /admin/api/channels` | Channel list |
| `GET /admin/api/logs` | Request logs |

## Architecture

```
Client (any protocol) → ai-adapter → Channel Selection → Key Pool → Upstream LLM
                                ↓
                          Capability Router
                   (native forward or best-cost conversion)
                                ↓
                          Translation Layer
                (Chat ↔ Responses ↔ Claude ↔ Gemini)
```

## Build Scripts

```bash
# Windows
.\scripts\build.ps1 -All         # All platforms
.\scripts\build.ps1 -Windows     # Windows only

# Linux / macOS
./scripts/build.sh               # All platforms
./scripts/build.sh linux         # Linux only
```

Output: `dist/ai-adapter-{os}-{arch}[.exe]`

## License

MIT