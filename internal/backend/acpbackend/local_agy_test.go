package acpbackend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
)

// TestLocalAgyACPHandsakeOnHost runs a live integration check against the real `agy` CLI
// on this machine in a completely isolated temporary profile directory. It runs
// only with APX_LIVE_AGY=1, so `go test` doesn't depend on the host.
func TestLocalAgyACPHandsakeOnHost(t *testing.T) {
	if os.Getenv("APX_LIVE_AGY") != "1" {
		t.Skip("set APX_LIVE_AGY=1 to run against the installed agy CLI")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skip("agy CLI not installed on host; skipping live local test")
	}

	dataDir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dataDir)
	mgr := NewManager(dataDir, reg, nil, config.ToolActivityDrop)
	defer mgr.Close()

	acct := config.AccountConfig{
		ID:             "agy-localtest",
		Name:           "localtest",
		Kind:           config.KindACP,
		Backend:        "agy",
		Enabled:        true,
		PermissionMode: config.PermDeny,
		Models:         []string{"default"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Launching worker must complete `initialize` cleanly without any "must not be absolute" error!
	w, err := mgr.getOrStartWorker(ctx, acct)
	if err != nil {
		t.Fatalf("real agy acp initialize failed: %v", err)
	}
	if !w.alive() {
		t.Fatalf("expected agy worker to remain alive after initialize")
	}

	// Verify profile isolation: `.gemini` directory must be created inside dataDir/profiles/agy/localtest
	pdir, _ := config.ProfileDir(dataDir, "agy", "localtest")
	if _, err := os.Stat(filepath.Join(pdir, ".gemini")); err != nil {
		t.Fatalf("expected isolated .gemini dir inside %s: %v", pdir, err)
	}

	// Verify session/new either succeeds (if logged in) OR cleanly classifies as Auth ("Authentication required"),
	// NEVER failing with "must not be absolute".
	_, err = mgr.TestAccount(ctx, acct)
	if err != nil {
		if strings.Contains(err.Error(), "must not be absolute") {
			t.Fatalf("regression: agy rejected path flag: %v", err)
		}
		be, ok := backend.AsBackendError(err)
		if !ok || be.Kind != backend.ErrKindAuth {
			t.Fatalf("expected clean auth error on unauthenticated temp profile, got: %v", err)
		}
	}
}
