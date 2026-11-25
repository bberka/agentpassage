package connect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/router"
)

func TestConnectMergesConfigsSafely(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "acpx_connect_test_key"
	models := []router.ModelEntry{
		{ID: "gemini-4-neon"},
		{ID: "a-gemini-4-neon"},
		{ID: "b-gemini-4-neon"},
	}

	// 1. OpenCode JSON merge preserves existing keys + creates backup
	ocPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(ocPath, []byte(`{"theme":"dark","provider":{"existing":{"name":"keep"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tOC, _ := FindTarget("opencode")
	res, err := ApplyAtPath(tOC, ocPath, cfg, models, true)
	if err != nil || !res.Applied || res.Backup == "" {
		t.Fatalf("opencode connect failed: res=%+v err=%v", res, err)
	}
	raw, _ := os.ReadFile(ocPath)
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	provs := parsed["provider"].(map[string]any)
	if _, ok := provs["existing"]; !ok {
		t.Fatalf("expected existing provider preserved")
	}
	if _, ok := provs["acpx"]; !ok {
		t.Fatalf("expected acpx provider added")
	}

	// 2. Codex TOML upsert is idempotent
	codexPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(codexPath, []byte("model = \"o4-mini\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tCodex, _ := FindTarget("codex")
	if _, err := ApplyAtPath(tCodex, codexPath, cfg, models, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAtPath(tCodex, codexPath, cfg, models, true); err != nil {
		t.Fatal(err)
	}
	codexRaw, _ := os.ReadFile(codexPath)
	if strings.Count(string(codexRaw), "[model_providers.acpx]") != 1 {
		t.Fatalf("expected idempotent single acpx block, got:\n%s", codexRaw)
	}

	// 3. T3 Code provider file
	t3Path := filepath.Join(dir, "acpx-provider.json")
	tT3, _ := FindTarget("t3code")
	res3, err := ApplyAtPath(tT3, t3Path, cfg, models, true)
	if err != nil || !res3.Applied || !strings.Contains(res3.Snippet, "acp") {
		t.Fatalf("t3code connect failed: res=%+v err=%v", res3, err)
	}

	// 4. Claude Code settings merge
	clPath := filepath.Join(dir, "settings.json")
	tCl, _ := FindTarget("claude")
	if _, err := ApplyAtPath(tCl, clPath, cfg, models, true); err != nil {
		t.Fatal(err)
	}
	clRaw, _ := os.ReadFile(clPath)
	if !strings.Contains(string(clRaw), "ANTHROPIC_BASE_URL") {
		t.Fatalf("expected ANTHROPIC_BASE_URL in claude settings, got %s", clRaw)
	}

	// 5. Pi models.json merge
	piPath := filepath.Join(dir, "models.json")
	tPi, _ := FindTarget("pi")
	if _, err := ApplyAtPath(tPi, piPath, cfg, models, true); err != nil {
		t.Fatal(err)
	}
	piRaw, _ := os.ReadFile(piPath)
	if !strings.Contains(string(piRaw), "a-gemini-4-neon") {
		t.Fatalf("expected prefixed model in pi models.json, got %s", piRaw)
	}
}
