package hostmcp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOpenCodeAndGemini(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOCS_TOKEN", "abc")
	write(t, filepath.Join(home, ".config/opencode/opencode.json"), `{"mcp": {
		"timeout": 5000,
		"flat": {"type": "local", "command": ["/bin/flat", "mcp"], "environment": {"T": "{env:DOCS_TOKEN}"}},
		"off": {"type": "local", "command": ["/bin/off"], "enabled": false},
		"servers": {"nested": {"command": ["/bin/nested"]}}}}`)
	write(t, filepath.Join(home, ".config/opencode/opencode.jsonc"), `{
		// a comment
		"mcp": {"remote": {"type": "remote", "url": "https://example.test/a//b",}, /* more */ },
	}`)
	got := merged(t, home, "opencode", "", nil, Capabilities{HTTP: true})
	for _, want := range []string{`"command":"/bin/flat"`, `"args":["mcp"]`, `{"name":"T","value":"abc"}`, "/bin/nested", "https://example.test/a//b"} {
		if !strings.Contains(got, want) {
			t.Errorf("opencode servers lack %s: %s", want, got)
		}
	}
	if strings.Contains(got, "/bin/off") || strings.Contains(got, "timeout") {
		t.Errorf("opencode servers include a non-server: %s", got)
	}

	write(t, filepath.Join(home, ".gemini/settings.json"), `{"mcpServers": {
		"stream": {"httpUrl": "https://example.test/stream"},
		"events": {"url": "https://example.test/events"}}}`)
	got = merged(t, home, "gemini", "", nil, Capabilities{SSE: true})
	if !strings.Contains(got, "/events") || strings.Contains(got, "/stream") {
		t.Errorf("gemini transports are wrong: %s", got)
	}
}
