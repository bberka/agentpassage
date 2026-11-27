# Development

## Commands

| Command | Does |
|---|---|
| `make check` | `gofmt`, `go vet`, `go test -race ./...`, and macOS and Windows builds. CI runs the same |
| `make deploy` | Installs the current commit, restarts the user service and checks it serves that commit (Linux) |
| `make build` / `make install` | Builds `./apx` / installs it to `~/.local/bin` without restarting anything |
| `make test`, `make bench`, `make size`, `make cross` | Tests, benchmarks, binary size, release binaries in `dist/` |
| `apx routing explain <model>` | Shows which account a model would use, without spending quota |
| `apx version` | Version and the commit the binary was built from |

Set `APX_PUBLIC_URL=https://your.domain make deploy` to also check a public address.

`make deploy` lists processes still running an older binary. `apx acp` processes belong to the host app, so restart that app to update them.

`go test` skips the live Antigravity check unless `APX_LIVE_AGY=1` is set.

## Packages

| Path | Contents |
|---|---|
| `cmd/apx` | CLI commands and service entry points |
| `internal/gateway` | HTTP API, request execution, failover, alerts, host usage compatibility |
| `internal/acpbridge` | `apx acp`: the ACP server that hosts such as T3 Code talk to, and model names |
| `internal/backend` | Talking to accounts: ACP, CLI and HTTP, plus attachments |
| `internal/pool`, `internal/router` | Account selection and model resolution |
| `internal/quota`, `internal/usage`, `internal/health` | Provider limits, local usage, health probes |
| `internal/config`, `internal/secrets`, `internal/descriptors` | `config.toml`, keys, agent and provider presets |
| `internal/connect` | Writing other tools' configs |
| `internal/tui`, `internal/service` | Terminal UI and background service |
