package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/zalando/go-keyring"
)

func TestFileFallbackPermissionsAndRedaction(t *testing.T) {
	dir := t.TempDir()
	st := NewFileOnly(dir)
	if !st.UsingFileFallback() {
		t.Fatalf("expected file fallback true")
	}
	if err := st.Set("deepseek-default", "sk-secret-value-123456"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := st.Get("deepseek-default")
	if err != nil || got != "sk-secret-value-123456" {
		t.Fatalf("Get mismatch: got=%q err=%v", got, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "secrets.json"))
		if err != nil {
			t.Fatalf("stat secrets.json: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 permissions, got %o", info.Mode().Perm())
		}
	}

	if masked := Mask("sk-secret-value-123456"); strings.Contains(masked, "secret") {
		t.Fatalf("Mask leaked secret: %q", masked)
	}

	logs := logbuf.New(10, dir, st.KnownSecrets)
	defer logs.Close()
	logs.Error("request", "failed with Authorization: Bearer sk-secret-value-123456", nil)
	entries := logs.Entries()
	if len(entries) != 1 || strings.Contains(entries[0].Message, "sk-secret-value-123456") {
		t.Fatalf("logbuf leaked secret: %+v", entries)
	}
}

func TestKeychainSecretsStayOutOfTheFile(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(path, []byte(`{"old":"file-old-value","stale":"file-stale-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewFileOnly(dir)
	st.useKeyring = true
	if err := st.Set("kept", "keychain-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("stale", "keychain-new-value"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("old"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "{}" {
		t.Fatalf("secrets file should be empty, got %s", got)
	}

	st.useKeyring = false
	if err := st.Set("plain", "file-only-value"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "keychain-") || !strings.Contains(string(data), "file-only-value") {
		t.Fatalf("file holds the wrong secrets: %s", data)
	}
}
