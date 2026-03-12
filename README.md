# openai-bridge

`openai-bridge` is a small Go HTTP proxy that helps you use **OpenAI’s Responses API-style clients** against an **OpenAI-compatible upstream** that primarily supports **Chat Completions** (and Embeddings).

It does two things:

1. **Translates** `POST /v1/responses` (Responses API wire format) into an upstream **Chat Completions** request, then **translates the result back** into a Responses API response.
2. **Passes through everything else** to your upstream API via a reverse proxy (useful for `/v1/models`, etc.).

It also supports **SSE streaming** for `stream:true` on `/v1/responses` by converting upstream Chat Completions streaming chunks into Responses API streaming events.

## Features

- `POST /v1/responses` (and also `POST /responses`) translation layer:
  - Responses input supports:
    - Plain string input
    - Array input items including:
      - `message` items (`user`, `assistant`, `system`, `developer`)
      - `function_call` items
      - `tool_result` items (translated to chat “tool” messages)
  - Supports **function tools** (`tools: [{ type: "function", ... }]`) and tool choice translation.
  - Supports common generation params: `temperature`, `top_p`, `max_output_tokens`, `seed`, `metadata`, etc.
  - Supports streaming (`stream: true`) with Responses API-shaped SSE events.
- `POST /v1/embeddings` (and `POST /embeddings`) is handled directly and forwarded upstream using `openai-go`.
- Everything else is reverse-proxied to your upstream unchanged.
- Optional model mapping (client model name → upstream model name).
- Optional debug logging (logs request/response bodies).

## How it routes requests

The server normalizes paths so either style works:

- If your client calls `/v1/responses`, the bridge strips `/v1` and routes it as `/responses`.
- If your client calls `/responses` directly (treating the bridge as the “v1 base URL”), that works too.

Routing rules:

- `POST /v1/responses` or `POST /responses` → translated to upstream Chat Completions
- `POST /v1/embeddings` or `POST /embeddings` → handled as embeddings
- Anything else → reverse proxy passthrough to `upstream_base_url`

## Requirements

- Go toolchain (the repo’s `go.mod` specifies Go `1.25.5`).

## Installation / Build

Clone and build:

```bash
git clone https://github.com/snowie2000/openai-bridge.git
cd openai-bridge

go build -o openai-bridge .
```

Or run directly:

```bash
go run . -config config.yaml
```

## Configuration

The bridge reads a YAML config file (default: `config.yaml`).

Example `config.yaml`:

```yaml
# Address to listen on.
listen_addr: "127.0.0.1:1234"

# Upstream OpenAI-compatible base URL (must end without a trailing slash).
# The bridge will forward all non-translated requests here.
upstream_base_url: "http://127.0.0.1:7816"

# Optional: if set, the bridge always uses this key for upstream requests.
# If omitted, it will forward the client's Bearer token (if provided).
# api_key: "sk-..."

# Set to true to log raw request and response bodies.
debug: false

# Translate client model names to upstream model names.
# model_mapping:
#   gpt-4o: gemini-2.0-flash
#   o3-mini: gemini-3-flash-preview
```

Notes:

- If `listen_addr` is omitted, it defaults to `:8080`.
- If `upstream_base_url` is omitted, it defaults to `https://api.openai.com/v1`.
- **No trailing slash** on `upstream_base_url` is recommended/expected.

### Authentication behavior

You have two modes:

1. **Static upstream key** (`api_key` set in config)
   - The bridge **always** sends `Authorization: Bearer <api_key>` upstream.
   - Any incoming client Authorization header is ignored (for passthrough too).

2. **Pass-through client key** (`api_key` not set)
   - For translated endpoints (`/responses`, `/embeddings`), the bridge forwards the incoming `Authorization: Bearer ...` token upstream (if present).
   - For passthrough routes, the reverse proxy forwards the client’s Authorization header as-is.

## Run

Start the server:

```bash
./openai-bridge -config config.yaml
```

You should see a log like:

```
openai-bridge listening on 127.0.0.1:1234 → http://127.0.0.1:7816
```

## Usage

### Call the Responses API (non-streaming)

```bash
curl http://127.0.0.1:1234/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{
    "model": "gpt-4o",
    "input": "Say hello in Spanish."
  }'
```

If you configured `model_mapping.gpt-4o`, the bridge will rewrite the model before sending upstream.

### Call the Responses API (streaming)

```bash
curl -N http://127.0.0.1:1234/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{
    "model": "gpt-4o",
    "input": "Write a short limerick.",
    "stream": true
  }'
```

This returns `text/event-stream` where the bridge emits Responses-API-style events and ends with:

```
data: [DONE]
```

### Embeddings

```bash
curl http://127.0.0.1:1234/v1/embeddings \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{
    "model": "text-embedding-3-small",
    "input": "hello world"
  }'
```

The embeddings response is returned from upstream in OpenAI-compatible embeddings format.

### Passthrough examples

These requests are not translated—they are reverse proxied upstream:

```bash
curl http://127.0.0.1:1234/v1/models -H "Authorization: Bearer YOUR_KEY"
```

## Translation limitations (important)

Some Responses API fields are explicitly rejected (HTTP 400) because they require server-side state or features not implemented in this proxy, including:

- `previous_response_id`
- `conversation`
- `background`
- `prompt` (prompt templates)

Tools:

- Only `tools[].type == "function"` is supported. Other tool types (file_search, web_search, code_interpreter, computer_use, mcp, etc.) are rejected.

Content:

- Image parts in some places may not be translatable to Chat Completions depending on where they appear (the bridge supports user image parts for chat-completions translation, but will reject unsupported content types in other contexts).

## Development notes

Project structure:

- `main.go` — CLI entrypoint, config loading, starts HTTP server
- `internal/config` — YAML config parsing and defaults
- `internal/proxy` — HTTP router + passthrough reverse proxy
- `internal/converter` — request/response translation logic + streaming translation

## License

No license file was found in the repository root at the time this README was generated. Add a `LICENSE` file if you intend to open-source this project under specific terms.
