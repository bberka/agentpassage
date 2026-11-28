package acpbackend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPiMCPIsSharedWithHostSessionsOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	home, profile := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(home, ".pi", "agent", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"settings":{"x":1},"mcpServers":{"ok":{"command":"ok-mcp"},"new":{"command":"new-mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	allow := func(name, _ string) bool { return name == "ok" }
	// An earlier build linked the whole file.
	dest := filepath.Join(profile, "mcp.json")
	if err := os.Symlink(source, dest); err != nil {
		t.Fatal(err)
	}

	env, err := piMCPEnv(profile, "", true, allow)
	if err != nil || len(env) != 0 {
		t.Fatalf("host session env = %v, %v", env, err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(got), "ok-mcp") || strings.Contains(string(got), "new-mcp") || !strings.Contains(string(got), `"settings"`) {
		t.Fatalf("mcp.json = %s, %v", got, err)
	}
	if info, err := os.Lstat(dest); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("mcp.json is not apx's own copy: %v", err)
	}
	if original, _ := os.ReadFile(source); !strings.Contains(string(original), "new-mcp") {
		t.Fatal("the user's mcp.json was changed")
	}

	// A file the account made itself stays.
	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, "mcp.json"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := piMCPEnv(own, "", true, allow); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "mcp.json")); string(b) != "mine" {
		t.Fatalf("the account's mcp.json was replaced: %s", b)
	}

	// The file is still there, so the API process must switch MCP off.
	env, err = piMCPEnv(profile, "/opt/it's/pi", false, allow)
	if err != nil || len(env) != 1 {
		t.Fatalf("API env = %v, %v", env, err)
	}
	path, ok := strings.CutPrefix(env[0], "PI_ACP_PI_COMMAND=")
	if !ok {
		t.Fatalf("API env = %v", env)
	}
	script, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(script), `'/opt/it'\''s/pi' --no-mcp "$@"`) {
		t.Fatalf("launcher = %q, %v", script, err)
	}
}
