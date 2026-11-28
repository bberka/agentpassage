package clibackend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/sharing"
)

func TestOnlyHostRunsGetTheUsersMCPServers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	home, bin, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude.json"), `{"mcpServers": {"mine": {"command": "/bin/claude-mcp", "env": {"TOKEN": "s3cret"}}}}`, 0o600)
	write(filepath.Join(home, ".codex/config.toml"), "[mcp_servers.mine]\ncommand = \"/bin/codex-mcp\"\n", 0o600)
	write(filepath.Join(home, ".config/opencode/opencode.json"), `{"mcp": {"mine": {"command": ["/bin/opencode-mcp"]}}}`, 0o600)
	// Each fake prints its arguments and whatever MCP config the run was given.
	fake := `#!/bin/sh
out="$*"
prev=
for a in "$@"; do
	[ "$prev" = --mcp-config ] && out="$out $(cat "$a")"
	[ "$prev" = -p ] && [ -n "$CODEX_HOME" ] && [ -f "$CODEX_HOME/$a.config.toml" ] && out="$out $(cat "$CODEX_HOME/$a.config.toml")"
	prev=$a
done
printf '%s %s' "$out" "$OPENCODE_CONFIG_CONTENT" | tr '\n' ' ' > "` + bin + `/seen"
`
	for _, agent := range []string{"claude", "codex", "opencode"} {
		write(filepath.Join(bin, agent), fake, 0o700)
	}
	// Only the approved servers are shared.
	if _, err := sharing.Open(data, home, []config.AccountConfig{
		{Kind: config.KindCLI, Backend: "claude"}, {Kind: config.KindCLI, Backend: "codex"}, {Kind: config.KindCLI, Backend: "opencode"},
	}); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, ".claude.json"), `{"mcpServers": {"mine": {"command": "/bin/claude-mcp", "env": {"TOKEN": "s3cret"}}, "added": {"command": "/bin/added-mcp"}}}`, 0o600)
	reg, _ := descriptors.LoadRegistry(data)
	m := NewManager(data, reg, nil)
	for _, agent := range []string{"claude", "codex", "opencode"} {
		for _, host := range []bool{true, false} {
			acct := config.AccountConfig{ID: agent + "/a", Backend: agent, Name: "a"}
			req := backend.ChatRequest{Messages: []backend.Message{{Role: "user", Content: "hi"}}, HostTools: host, Cwd: home}
			if err := m.Stream(context.Background(), acct, req, func(backend.StreamChunk) error { return nil }); err != nil {
				t.Fatal(err)
			}
			seen, err := os.ReadFile(filepath.Join(bin, "seen"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(seen), "/bin/"+agent+"-mcp"); got != host {
				t.Errorf("%s host=%v: MCP server given = %v: %s", agent, host, got, seen)
			}
			if strings.Contains(string(seen), "/bin/added-mcp") {
				t.Errorf("%s got a server that wasn't approved", agent)
			}
			if args, _, _ := strings.Cut(string(seen), "{"); strings.Contains(args, "s3cret") {
				t.Errorf("%s put a secret in its arguments", agent)
			}
		}
	}
	left, _ := filepath.Glob(filepath.Join(data, "profiles", "*", "*", "apx-mcp-*"))
	if len(left) != 0 {
		t.Errorf("MCP config left in a profile: %v", left)
	}
}
