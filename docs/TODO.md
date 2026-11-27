# Open work

## Security

- Check whether `apx acp` should forward permission requests to the host when the host sets no mode.

## Reliability

- Lock `config.toml` across processes while saving, so concurrent saves can't drop each other's changes.
- Fail an API-key model's turn when it returns tool-call markup as text.
- Pass tool calls through `/v1/responses`, so Codex can use tools with API-key models.
- Give up on a silent ACP or CLI agent. Only HTTP providers have a silence limit, since an agent turn can wait long on tools or permission.
- Stop `agy`'s internal 429 retries early, if they can be seen. Its local logs showed quota errors but no retry loop to act on.

## Agent profiles

- Share Claude and Codex plugins.
- Give the `agy`, `grok`, `cursor` and `gemini` CLI backends the shared MCP servers without exposing them to API requests.
- Test MCP and skills sharing live with Pi, Cursor, Gemini, Cline, Muse Code, Kilo, Goose and Amp accounts.
- The `cursor` backend runs `agent`, which Grok also installs under that name. Detect Cursor by `cursor-agent`.

## Tests

- Fixtures for the fields T3 Code reads (see [t3-code-integration.md](t3-code-integration.md)).
- TUI render tests at 80x24 and a wide size.
- Run staticcheck and govulncheck in CI.

## Cleanup

- Decide whether to remove the unused release workflow and GoReleaser config.
- Stamp the commit in `install.ps1`.
