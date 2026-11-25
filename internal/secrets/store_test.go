package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bberka/acpx/internal/logbuf"
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
