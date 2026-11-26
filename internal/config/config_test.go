package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSaveAndProfileIsolation(t *testing.T) {
	dir := t.TempDir()
	cfg, created, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true on first load")
	}
	if cfg.Server.Bind != DefaultBind || cfg.Server.Port != DefaultPort {
		t.Fatalf("unexpected server defaults: %+v", cfg.Server)
	}
	if cfg.Server.LocalKey == "" {
		t.Fatalf("expected generated local key")
	}

	cfg.Accounts = append(cfg.Accounts, AccountConfig{
		ID:      "agy-work",
		Name:    "work",
		Kind:    KindACP,
		Backend: "agy",
		Enabled: true,
		Models:  []string{"gemini-3.1-pro"},
	})
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, created2, err := Load(dir)
	if err != nil {
		t.Fatalf("second Load failed: %v", err)
	}
	if created2 {
		t.Fatalf("expected created=false on second load")
	}
	if len(loaded.Accounts) != 1 || loaded.Accounts[0].PermissionMode != PermAutoApprove || loaded.Accounts[0].MaxConcurrency != 1 {
		t.Fatalf("unexpected normalized account: %+v", loaded.Accounts)
	}

	// Path traversal rejected
	if _, err := ProfileDir(dir, "agy", "../escape"); err == nil {
		t.Fatalf("expected error for traversal account name")
	}

	pdir, err := EnsureProfileDir(dir, "agy", "work")
	if err != nil {
		t.Fatalf("EnsureProfileDir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "cred.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write cred: %v", err)
	}
	if err := DeleteProfileDir(dir, "agy", "work"); err != nil {
		t.Fatalf("DeleteProfileDir failed: %v", err)
	}
	if _, err := os.Stat(pdir); !os.IsNotExist(err) {
		t.Fatalf("expected profile dir deleted")
	}
}

func TestAccountModelCatalogEnableDisableAndMerge(t *testing.T) {
	acct := AccountConfig{
		ID:      "deepseek-default",
		Name:    "default",
		Kind:    KindAPIKey,
		Backend: "deepseek",
		Models:  []string{"deepseek-chat"},
	}

	acct.MergeDiscoveredModels([]string{"deepseek-chat", "deepseek-reasoner", "deepseek-coder"})
	if len(acct.AvailableModels) != 3 || len(acct.Models) != 3 {
		t.Fatalf("expected 3 catalog & 3 enabled, got avail=%v enabled=%v", acct.AvailableModels, acct.Models)
	}

	// Disable one model and verify re-fetching does NOT re-enable it
	acct.SetModelEnabled("deepseek-coder", false)
	if len(acct.Models) != 2 || len(acct.DisabledModels) != 1 {
		t.Fatalf("expected 2 enabled / 1 disabled, got enabled=%v disabled=%v", acct.Models, acct.DisabledModels)
	}

	acct.MergeDiscoveredModels([]string{"deepseek-chat", "deepseek-reasoner", "deepseek-coder", "deepseek-v4"})
	if len(acct.AvailableModels) != 4 || len(acct.Models) != 3 {
		t.Fatalf("expected disabled model preserved and new model enabled, got avail=%v enabled=%v", acct.AvailableModels, acct.Models)
	}

	acct.SetAllModelsEnabled(false)
	if len(acct.Models) != 0 || len(acct.DisabledModels) != 4 {
		t.Fatalf("expected all disabled, got enabled=%v disabled=%v", acct.Models, acct.DisabledModels)
	}
	acct.SetAllModelsEnabled(true)
	if len(acct.Models) != 4 || len(acct.DisabledModels) != 0 {
		t.Fatalf("expected all enabled, got enabled=%v disabled=%v", acct.Models, acct.DisabledModels)
	}
}
