package hostmcp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClineMuseKiloGooseAmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOCS_TOKEN", "abc")
	write(t, filepath.Join(home, ".cline/data/settings/cline_mcp_settings.json"), `{"mcpServers": {
		"stream": {"type": "streamableHttp", "url": "https://example.test/stream"},
		"events": {"url": "https://example.test/events"},
		"off": {"command": "/bin/off", "disabled": true}}}`)
	write(t, filepath.Join(home, ".config/muse/settings.json"), `{"schema_version": 1, "mcp_servers": {
		"on": {"transport": "stdio", "command": "/bin/muse-mcp", "enabled": true},
		"web": {"transport": "streamable_http", "url": "https://example.test/muse"},
		"off": {"command": "/bin/off", "enabled": false}}}`)
	write(t, filepath.Join(home, ".config/kilo/kilo.jsonc"), `{"mcp": {"k": {"type": "local", "command": ["/bin/kilo-mcp"]},}}`)
	write(t, filepath.Join(home, ".config/goose/config.yaml"), `
extensions:
  files: {type: stdio, cmd: /bin/goose-mcp, args: [serve], envs: {A: b}, enabled: true}
  web: {type: streamable_http, uri: "https://example.test/goose", enabled: true}
  developer: {type: builtin, enabled: true}
  off: {type: stdio, cmd: /bin/off, enabled: false}
`)
	write(t, filepath.Join(home, ".config/amp/settings.json"), `{"amp.mcpServers": {"a": {"command": "/bin/amp-mcp", "env": {"T": "${DOCS_TOKEN}"}}}}`)

	for agent, want := range map[string][]string{
		"cline":     {`"name":"stream","type":"http"`, `"name":"events","type":"sse"`},
		"muse-code": {"/bin/muse-mcp", `"name":"web","type":"http"`},
		"kilo":      {"/bin/kilo-mcp"},
		"goose":     {"/bin/goose-mcp", `"args":["serve"]`, "https://example.test/goose"},
		"amp-acp":   {"/bin/amp-mcp", `{"name":"T","value":"abc"}`},
	} {
		got := merged(t, home, agent, "", nil, Capabilities{HTTP: true, SSE: true})
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s servers lack %s: %s", agent, w, got)
			}
		}
		if strings.Contains(got, "/bin/off") || strings.Contains(got, "developer") {
			t.Errorf("%s includes a disabled or built-in server: %s", agent, got)
		}
	}
}
