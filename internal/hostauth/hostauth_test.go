package hostauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectAndCopyToProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	// Seed mock host credentials for Claude, Codex, Grok, OpenCode, and keyring-only Antigravity
	mustWrite := func(rel, body string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".claude/.credentials.json", `{"token":"claude-test"}`)
	mustWrite(".codex/auth.json", `{"token":"codex-test"}`)
	mustWrite(".grok/auth.json", `{"token":"grok-test"}`)
	mustWrite(".local/share/opencode/auth.json", `{"token":"oc-test"}`)
	mustWrite(".gemini/antigravity-cli/cache/onboarding.json", `{"done":true}`)

	all := DetectAll(home)
	if len(all) != 5 {
		t.Fatalf("expected 5 detected host logins, got %d: %+v", len(all), all)
	}

	// Copy Claude login into an isolated profile directory
	cl, ok := Detect("claude", home)
	if !ok || !cl.CanCopyFiles {
		t.Fatalf("expected copyable claude login, got %+v ok=%v", cl, ok)
	}
	profClaude := filepath.Join(t.TempDir(), "claude-default")
	if ProfileHasAuth("claude", profClaude) {
		t.Fatalf("expected empty profile before copy")
	}
	n, err := CopyToProfile(cl, profClaude)
	if err != nil || n != 1 {
		t.Fatalf("CopyToProfile failed: n=%d err=%v", n, err)
	}
	if !ProfileHasAuth("claude", profClaude) {
		t.Fatalf("expected ProfileHasAuth=true after copy")
	}
	fi, err := os.Stat(filepath.Join(profClaude, ".credentials.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 copied credential, got err=%v perm=%v", err, fi.Mode().Perm())
	}

	// Keyring-only Antigravity should be detected cleanly as KeyringOnly
	agy, ok := Detect("agy", home)
	if !ok || !agy.KeyringOnly || agy.CanCopyFiles {
		t.Fatalf("expected keyring-only agy detection, got %+v ok=%v", agy, ok)
	}
	if _, err := CopyToProfile(agy, t.TempDir()); err == nil {
		t.Fatalf("expected CopyToProfile to refuse keyring-only login")
	}
}
