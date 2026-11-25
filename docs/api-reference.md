# API Reference

All `/v1/*` endpoints require `Authorization: Bearer <key>` or `x-api-key: <key>`. Admin endpoints (`/admin/*`) require the primary key or an `admin`-role API key.

## Inference Endpoints

### `GET /v1/models`
Returns enabled models across all configured accounts (filtered by key allowlist if the API key restricts models).

### `POST /v1/chat/completions`
OpenAI-compatible chat completions (streaming SSE and non-streaming). Supports `reasoning_effort`, project routing, account failover, model fallback, and reversible privacy filtering.

### `POST /v1/messages`
Anthropic Messages API compatible endpoint (streaming SSE and non-streaming), including `thinking` and `output_config.effort`.

### `POST /v1/embeddings`
OpenAI-compatible embeddings endpoint routed to HTTP API-key providers:

```sh
curl http://127.0.0.1:8787/v1/embeddings \
  -H "Authorization: Bearer $APX_KEY" \
  -H "X-Apx-Project: search" \
  -H "Content-Type: application/json" \
  -d '{"model":"text-embedding-3-small","input":["hello","world"]}'
```

### `POST /v1/rerank`
Standard Cohere/Jina/vLLM-compatible reranking endpoint routed to HTTP API-key providers:

```sh
curl http://127.0.0.1:8787/v1/rerank \
  -H "Authorization: Bearer $APX_KEY" \
  -H "X-Apx-Project: search" \
  -H "Content-Type: application/json" \
  -d '{"model":"rerank-v3","query":"local gateway","documents":["cats","agentpassage proxy"],"top_n":2}'
```

## Request & Response Headers

- **Request headers:**
  - `X-Apx-Project` (or legacy `X-Acpx-Project`): project identifier for project-based routing and usage attribution. Ignored when the API key is already pinned to a specific project.
  - `X-Apx-Session` (or legacy `X-Acpx-Session`): session/thread key for `sticky` and `round-robin` session affinity.
  - `X-Apx-Cwd`: working directory passed to ACP agents.
- **Response headers:**
  - `X-Apx-Account`: `<backend>/<account>` that served the request.
  - `X-Apx-Model`: actual model that served the request.
  - `X-Apx-Fallback-From`: original requested model when a fallback model handled the request.
  - `X-Apx-Project`: effective project identifier when set.
  - `X-Apx-Redacted`: category counts (e.g. `api_key=1,email=1`) when the privacy filter redacted values.

## Health & Admin Endpoints

- `GET /healthz` — lightweight liveness + account health summary (unauthenticated).
- `GET /usage` — local web Usage Report Hub (unlocks with admin key).
- `GET /admin/state` — accounts, banners, exposed model count, secrets backend.
- `GET /admin/usage?period=1h|24h|7d|30d|all` — requests, tokens, latency, estimated costs, and breakdowns by account, model, API key, and project.
- `GET /admin/quota` & `POST /admin/quota/refresh` — live subscription limits, API balances, and rolling 5h/7d counts.
- `GET /admin/health` & `POST /admin/health/check` — live account health probes.
- `GET /admin/logs` — redacted recent request and system logs.
- `POST /admin/reload` — hot-reload `config.toml` and secrets from disk.
