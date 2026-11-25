# HTTP API Reference

Base URL (default): `http://127.0.0.1:8787`

## Authentication

All `/v1/*` and `/admin/*` routes require your local `acpx` key (find it on the Dashboard or via `acpx export-env`):

- OpenAI style: `Authorization: Bearer acpx_...`
- Anthropic style: `x-api-key: acpx_...`

`/healthz` does not require authentication.

## Endpoints

### `GET /healthz`

Returns server liveness and version.

```json
{"status": "ok", "version": "0.1.0"}
```

### `GET /admin/state`

Returns live pool state, account health, banners, and secret backend info.

```json
{
  "running": true,
  "address": "127.0.0.1:8787",
  "bind": "127.0.0.1",
  "port": 8787,
  "secrets_backend": "OS keychain",
  "models_count": 4,
  "accounts": [...],
  "banners": [...]
}
```

### `GET /v1/models`

Returns OpenAI-compatible merged model list (`<backend>/<model>`, `<backend>/<account>/<model>`, and configured aliases, excluding hidden models).

### `POST /v1/chat/completions`

OpenAI-compatible chat completions (streaming SSE when `"stream": true`, JSON otherwise).

Supported custom request headers:
- `X-Acpx-Session`: Stable conversation identifier for sticky routing and ACP session reuse (falls back to body `"user"`).
- `X-Acpx-Cwd`: Working directory passed to ACP `session/new`.

Response headers:
- `X-Acpx-Account`: `<backend>/<account>` that served the request.
- `X-Acpx-Session-Id`: Underlying ACP session ID (ACP backends only).
- `X-Acpx-Tools-Ignored: true`: Present when the request included `tools`/`functions` and was routed to an ACP backend.

### `POST /v1/messages`

Anthropic Messages API-compatible endpoint (`system`, `messages`, `max_tokens`, `temperature`, `stream`, `metadata.user_id`). Works against any configured backend — Anthropic, OpenAI-compatible HTTP providers, or ACP subscription agents.

Supports both non-streaming JSON responses and Anthropic SSE (`message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`).
