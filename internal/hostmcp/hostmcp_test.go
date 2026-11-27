package hostmcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func merged(t *testing.T, home, agent, cwd string, sent []any, caps Capabilities) string {
	t.Helper()
	servers, err := Load(home, agent, cwd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(Merge(sent, servers, caps))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadAndMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("DOCS_TOKEN", "abc")
	write(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {"browse": {"command": "/bin/browse", "args": ["mcp"]}, "taken": {"command": "/bin/mine"}},
		"projects": {"/work": {"mcpServers": {"local": {"command": "/bin/local"}}},
		             "/other": {"mcpServers": {"elsewhere": {"command": "/bin/no"}}}}}`)
	write(t, filepath.Join(home, ".codex/config.toml"), `
[mcp_servers.off]
command = "/bin/off"
enabled = false
[mcp_servers.docs]
url = "https://example.test/mcp"
bearer_token_env_var = "DOCS_TOKEN"
`)

	got := merged(t, home, "claude", "/work", []any{map[string]any{"name": "taken", "command": "/bin/host"}}, Capabilities{})
	for _, want := range []string{`"name":"browse"`, `"args":["mcp"]`, `"name":"local"`, `"/bin/host"`, `{"name":"HOME","value":"` + home + `"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("claude servers lack %s: %s", want, got)
		}
	}
	for _, unwanted := range []string{"/bin/mine", "elsewhere"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("claude servers include %s: %s", unwanted, got)
		}
	}

	if got := merged(t, home, "codex", "/work", nil, Capabilities{}); got != "null" {
		t.Errorf("disabled and unsupported servers were sent: %s", got)
	}
	got = merged(t, home, "codex", "/work", nil, Capabilities{HTTP: true})
	want := `[{"headers":[{"name":"Authorization","value":"Bearer abc"}],"name":"docs","type":"http","url":"https://example.test/mcp"}]`
	if got != want {
		t.Errorf("codex servers = %s, want %s", got, want)
	}
}

func TestLoadErrorHidesFileContent(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".grok/config.toml"), `token = "s3cret`)
	if _, err := Load(home, "grok", ""); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error = %v", err)
	}
}
