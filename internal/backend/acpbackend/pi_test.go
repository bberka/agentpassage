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
	if err := os.WriteFile(source, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env, err := piMCPEnv(profile, "", true)
	if err != nil || len(env) != 0 {
		t.Fatalf("host session env = %v, %v", env, err)
	}
	if got, err := os.Readlink(filepath.Join(profile, "mcp.json")); err != nil || got != source {
		t.Fatalf("mcp.json link = %q, %v", got, err)
	}

	// The link is still there, so the API process must switch MCP off.
	env, err = piMCPEnv(profile, "/opt/it's/pi", false)
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
