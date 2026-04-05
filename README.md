# agentfoundry-worker

A Temporal worker that executes AI agent workflows. Each agent run is backed by an OpenAI-compatible LLM API (OpenRouter, OpenAI, Ollama, Together AI, Azure OpenAI, etc.) and can invoke tools via the agentfoundry backend. The multi-turn conversation loop runs as a durable Temporal workflow, making every LLM call and tool invocation replayable and resilient to failures.

The worker connects to [agentfoundry](https://github.com/angoo/agentfoundry) to resolve agent definitions, discover tools, and publish stream events. It does not handle user authentication or agent management — that is the backend's responsibility.

## Architecture

```
                     ┌──────────────────────────────────────┐
                     │        Temporal Frontend             │
                     │        (task queue: agentfoundry-worker)
                     └──────────────┬───────────────────────┘
                                    │
                     ┌──────────────▼───────────────────────┐
                     │           Temporal Worker            │
                     │                                      │
                     │  RunAgentWorkflow                    │
                     │    ├── ResolveAgentActivity          │
                     │    ├── ConnectEphemeralActivity      │
                     │    ├── BuildToolDefsActivity         │
                     │    ├── LLMChatActivity               │
                     │    ├── CallToolActivity              │
                     │    └── CallEphemeralToolActivity     │
                     │                                      │
                     │  Dependencies:                       │
                     │    ├── Registry (agent definitions)  │
                     │    ├── MCP Client Pool               │
                     │    └── LLM Client                    │
                     └──────────────────────────────────────┘
```

## Concepts

**Agent** — A YAML definition combining a system prompt, an LLM model, and a set of tools. The worker fetches the definition from the agentfoundry backend at the start of each workflow run.

**Tool** — A capability from an external MCP server, referenced as `server.tool` (e.g. `srvd.searxng_web_search`). Tool discovery and MCP execution go through the agentfoundry backend. Agents can also call other agents as tools.

**Workflow** — Each agent run executes as a Temporal workflow (`RunAgentWorkflow`). Individual steps (LLM calls, tool invocations) are Temporal activities, making the entire run durable and replayable.

## Quick Start

### Prerequisites

- Go 1.21+
- A running Temporal server
- A running [agentfoundry](https://github.com/angoo/agentfoundry) backend
- An API key for any OpenAI-compatible provider

### Build and Run

```bash
go build -o worker ./cmd/worker/
export TEMPORAL_HOST_PORT="localhost:7233"
export LLM_API_KEY="sk-or-..."
./worker
```

### Docker

```bash
docker build -t agentfoundry-worker .
docker run \
  -e TEMPORAL_HOST_PORT="temporal:7233" \
  -e LLM_API_KEY="sk-or-..." \
  -v $(pwd)/worker.yaml:/data/worker.yaml \
  -v $(pwd)/definitions:/data/definitions \
  agentfoundry-worker
```

## Configuration

### worker.yaml

```yaml
definitions_dir: "./definitions"

temporal:
  host_port: "localhost:7233"
  namespace: "default"
  # api_key: "${TEMPORAL_API_KEY}"

llm:
  base_url: "https://openrouter.ai/api/v1"
  api_key: "${OPENROUTER_API_KEY}"
  default_model: "openai/gpt-4o"
  headers:
    HTTP-Referer: "https://github.com/angoo/agentfoundry-worker"
    X-Title: "agentfoundry-worker"

mcp_servers:
  - name: "srvd"
    url: "https://mcp.srvd.dev/mcp"
    transport: "streamable-http"
```

### Agent Definition

Agent definitions are YAML files in the `definitions/` directory. They are hot-reloaded when changed.

```yaml
kind: agent
name: researcher
description: "Researches topics by searching the web and summarizing findings"
model: openai/gpt-4o
system_prompt: |
  You are a research assistant. Search the web for information
  and produce well-organized research briefs.
tools:
  - srvd.searxng_web_search
  - srvd.web_url_read
  - summarizer
max_turns: 15
```

### Environment Variables

All config values can be set via YAML, `${ENV_VAR}` syntax in YAML, or dedicated env vars. Priority: YAML value > env var in YAML > dedicated env var > default.

| Variable | Description | Default |
|----------|-------------|---------|
| `TEMPORAL_HOST_PORT` | Temporal frontend address | `localhost:7233` |
| `TEMPORAL_NAMESPACE` | Temporal namespace | `default` |
| `TEMPORAL_API_KEY` | Temporal API key | — |
| `LLM_BASE_URL` | OpenAI-compatible LLM API base URL | `https://openrouter.ai/api/v1` |
| `LLM_DEFAULT_MODEL` | Default LLM model | `openai/gpt-4o` |
| `LLM_API_KEY` | LLM API key | — |
| `OPENROUTER_API_KEY` | LLM API key (legacy fallback) | — |
| `ORCHESTRATOR_URL` | agentfoundry backend URL | `http://localhost:3000` |
| `ORCHESTRATOR_API_KEY` | Orchestrator API key | — |

## Project Structure

```
agentfoundry-worker/
├── cmd/worker/main.go           # Worker entrypoint
├── internal/
│   ├── config/                  # System config, agent definitions, YAML loader
│   ├── registry/                # Agent definition store
│   ├── mcpclient/               # MCP client pool (connects to external servers)
│   ├── llm/                     # OpenAI-compatible LLM client
│   └── temporal/                # Temporal workflows and activities
├── definitions/                 # Agent YAML definitions (hot-reloaded)
├── worker.example.yaml          # Example system configuration
├── Dockerfile
└── go.mod
```
