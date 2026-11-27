# Agent rules

AgentPassage (`apx`) is a Go gateway that serves several AI accounts through HTTP and ACP. These rules are for coding agents working in this repo. Commands and layout are in [docs/development.md](docs/development.md).

## Product rules

- apx is a gateway, never a chat app or agent harness. The host (T3 Code, an IDE) owns permissions; pass its choice through.
- Subscription accounts use primary-first failover. Don't enable round-robin or least-busy for them by default, and warn about ban risk when a user turns one on.
- A conversation switches accounts when its account runs out of quota. Waiting for the reset is an option that is off by default.
- Quota routing follows the limit that meters the model: agy Claude and GPT models use the Claude & GPT group, agy Gemini ranks by weekly, and the Claude Fable limit applies only to Fable.
- Model names show the provider (`AGY Claude Sonnet 5.5`, `Codex GPT-6 Luna`), plus the account name when models are listed per account.
- Install is by script or from source. There is no release process in use.
- Keep the binary small and add no runtime dependencies.

## Interface rules

- TUI: one line per list row, every screen's keys in the footer only, plain words, and no free-text model editing where a picker works.
- Preview and confirm before changing another tool's config.
- Notifications are one short title and at most one short line.

## Workflow

- When the user asks a question or asks what's next, answer without changing anything.
- One writer per checkout. Give parallel writing agents their own worktree.
- Search with `grep -n`, then read the line range you need. Don't print whole test files or large generated output.
- Run `make check` before every commit.
- Commit tested work in focused commits and push it to `main` without being asked. Subjects are imperative, at most 72 characters, with a body only to explain why. No AI co-author trailers. Never rewrite pushed history without approval.
- To update the running install, use `make deploy`. It fails if the tree is dirty or unpushed, or if the new build can't load the config or isn't the one serving.

## Reporting

- Say "done", "running" or "pushed" only for what you checked. For a feature, name the caller and the test or live output that shows it works.
- Keep open asks in [docs/TODO.md](docs/TODO.md): add one when you defer it, and remove it when it ships.
- Price changes go in `internal/usage/pricing.go` with `PricesChecked` updated, from the providers' own pricing pages.
- A host compatibility fix is done when a test asserts the fields the host reads. Record them in [docs/t3-code-integration.md](docs/t3-code-integration.md).
- If an earlier answer was wrong, say so.

## Delegation

- Accept a delegated result only with evidence it ran: a diff, and test output. Check a smaller model's root-cause claim yourself.
- API-key models (for example DeepSeek) get no tools through apx, so don't give them work that needs files or commands.

## Safety

- Never print or inline API keys. Read them into a variable, and never paste config files that contain them.
- A key that can call an ACP or CLI model can run that agent on the host. Say so whenever remote access or key sharing comes up.
- Don't edit `~/.config/agentpassage/config.toml` by hand while apx processes are running; use `apx` commands.
- Tests must not use the real data directory, send model requests, or show desktop notifications.
