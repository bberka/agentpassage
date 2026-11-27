package backend

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestIsPublicIPRefusesInternalRanges(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "100.100.100.100", "::ffff:100.100.100.100", "64:ff9b::a00:1", "169.254.1.1", "::1"} {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s must be refused", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "100.128.0.1", "2606:4700:4700::1111"} {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
}

func TestEnsurePrivateDirRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("a symlinked attachment directory must be refused")
	}
	if err := ensurePrivateDir(filepath.Join(base, "new")); err != nil {
		t.Fatal(err)
	}
}
