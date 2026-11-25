package connect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/router"
)

func TestConnectMergesConfigsSafely(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_connect_test_key"
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
	if _, ok := provs["apx"]; !ok {
		t.Fatalf("expected apx provider added")
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
	if strings.Count(string(codexRaw), "[model_providers.apx]") != 1 {
		t.Fatalf("expected idempotent single apx block, got:\n%s", codexRaw)
	}

	// 3. T3 Code settings.json merge separates commandPath and commandArgs: ["acp"] and keeps snippet concise
	t3Path := filepath.Join(dir, "t3-settings.json")
	if err := os.WriteFile(t3Path, []byte(`{"favorites":[],"providerInstances":{"pi":{"driver":"pi","enabled":true},"apx":{"driver":"acpRegistry","displayName":"My AgentPassage","config":{"commandPath":"/bad/apx acp","commandArgs":[]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tT3, _ := FindTarget("t3code")
	res3, err := ApplyAtPath(tT3, t3Path, cfg, models, true)
	if err != nil || !res3.Applied || !strings.Contains(res3.Snippet, "Command Args: acp") {
		t.Fatalf("t3code connect failed: res=%+v err=%v", res3, err)
	}
	if strings.Contains(res3.Snippet, "a-gemini-4-neon") {
		t.Fatalf("expected concise t3code snippet without listing all models, got:\n%s", res3.Snippet)
	}
	t3Raw, _ := os.ReadFile(t3Path)
	var t3Parsed map[string]any
	if err := json.Unmarshal(t3Raw, &t3Parsed); err != nil {
		t.Fatal(err)
	}
	t3Provs := t3Parsed["providerInstances"].(map[string]any)
	if _, ok := t3Provs["pi"]; !ok {
		t.Fatalf("expected existing T3 Code provider pi preserved")
	}
	t3Acpx := t3Provs["apx"].(map[string]any)
	if t3Acpx["displayName"] != "My AgentPassage" {
		t.Fatalf("expected custom displayName preserved, got %v", t3Acpx["displayName"])
	}
	t3Cfg := t3Acpx["config"].(map[string]any)
	if strings.Contains(t3Cfg["commandPath"].(string), " ") {
		t.Fatalf("commandPath must not include args, got %q", t3Cfg["commandPath"])
	}
	t3Args := t3Cfg["commandArgs"].([]any)
	if len(t3Args) != 1 || t3Args[0] != "acp" {
		t.Fatalf("expected commandArgs [acp], got %v", t3Args)
	}

	// 3b. Zed JSONC settings.json merge (with // comments & trailing commas)
	zedPath := filepath.Join(dir, "zed-settings.json")
	zedJSONC := "// Zed settings\n{\n  \"theme\": {\n    \"dark\": \"One Dark\",\n  },\n  \"context_servers\": {\n    \"browse\": { \"command\": \"/usr/bin/browse\" },\n  },\n}\n"
	if err := os.WriteFile(zedPath, []byte(zedJSONC), 0o600); err != nil {
		t.Fatal(err)
	}
	tZed, ok := FindTarget("zed")
	if !ok {
		t.Fatalf("expected zed target registered")
	}
	resZed, err := ApplyAtPath(tZed, zedPath, cfg, models, true)
	if err != nil || !resZed.Applied || resZed.Backup == "" {
		t.Fatalf("zed connect failed: res=%+v err=%v", resZed, err)
	}
	zedRaw, _ := os.ReadFile(zedPath)
	var zedParsed map[string]any
	if err := json.Unmarshal(zedRaw, &zedParsed); err != nil {
		t.Fatalf("zed output not valid JSON: %v\n%s", err, zedRaw)
	}
	if _, ok := zedParsed["context_servers"]; !ok {
		t.Fatalf("expected existing context_servers preserved in zed settings")
	}
	zedServers := zedParsed["agent_servers"].(map[string]any)
	if _, ok := zedServers["apx"]; !ok {
		t.Fatalf("expected agent_servers.apx in zed settings, got %s", zedRaw)
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

	// 5. Pi models.json merge excludes self 'pi' models (loop prevention) and sets X-Apx-Client header
	piModels := append([]router.ModelEntry(nil), models...)
	piModels = append(piModels, router.ModelEntry{ID: "pi/default", Backend: "pi"})
	piPath := filepath.Join(dir, "models.json")
	tPi, _ := FindTarget("pi")
	resPi, err := ApplyAtPath(tPi, piPath, cfg, piModels, true)
	if err != nil {
		t.Fatal(err)
	}
	piRaw, _ := os.ReadFile(piPath)
	if !strings.Contains(string(piRaw), "a-gemini-4-neon") {
		t.Fatalf("expected prefixed model in pi models.json, got %s", piRaw)
	}
	if strings.Contains(string(piRaw), `"pi/default"`) {
		t.Fatalf("loop guard failed: pi models.json must exclude self 'pi' backend models, got %s", piRaw)
	}
	if !strings.Contains(string(piRaw), `"X-Apx-Client": "pi"`) || !strings.Contains(resPi.Message, "loop guard") {
		t.Fatalf("expected X-Apx-Client header and loop guard note, msg=%q raw=%s", resPi.Message, piRaw)
	}

	// 6. VS Code chatLanguageModels.json merge + disconnect preserves existing entries
	vsPath := filepath.Join(dir, "chatLanguageModels.json")
	if err := os.WriteFile(vsPath, []byte(`[{"name":"existing-local","vendor":"customendpoint","models":[]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	tVS, ok := FindTarget("vscode")
	if !ok {
		t.Fatalf("expected vscode target")
	}
	if _, err := ApplyAtPath(tVS, vsPath, cfg, models, true); err != nil {
		t.Fatalf("vscode connect failed: %v", err)
	}
	if !IsConnectedAtPath("vscode", vsPath) {
		t.Fatalf("expected IsConnectedAtPath(vscode) == true")
	}
	// Disconnect VS Code -> removes apx, keeps existing-local
	dVS, err := DisconnectAtPath(tVS, vsPath)
	if err != nil || !dVS.Removed {
		t.Fatalf("vscode disconnect failed: res=%+v err=%v", dVS, err)
	}
	if IsConnectedAtPath("vscode", vsPath) {
		t.Fatalf("expected IsConnectedAtPath(vscode) == false after disconnect")
	}
	vsRaw, _ := os.ReadFile(vsPath)
	if !strings.Contains(string(vsRaw), "existing-local") || strings.Contains(string(vsRaw), `"apx"`) {
		t.Fatalf("unexpected vscode file after disconnect: %s", vsRaw)
	}

	// 7. JetBrains ~/.jetbrains/acp.json connect + disconnect (and alias "rider")
	jbPath := filepath.Join(dir, "acp.json")
	tJB, ok := FindTarget("rider")
	if !ok || tJB.ID != "jetbrains" {
		t.Fatalf("expected rider alias -> jetbrains, got ok=%v id=%q", ok, tJB.ID)
	}
	if _, err := ApplyAtPath(tJB, jbPath, cfg, models, true); err != nil {
		t.Fatalf("jetbrains connect failed: %v", err)
	}
	if !IsConnectedAtPath("jetbrains", jbPath) {
		t.Fatalf("expected jetbrains connected")
	}
	dJB, err := DisconnectAtPath(tJB, jbPath)
	if err != nil || !dJB.Removed || IsConnectedAtPath("jetbrains", jbPath) {
		t.Fatalf("jetbrains disconnect failed: res=%+v err=%v", dJB, err)
	}

	// 8. Continue.dev YAML block + Aider YAML block + Neovim Lua connect & disconnect
	contPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(contPath, []byte("name: My Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tCont, _ := FindTarget("continue")
	if _, err := ApplyAtPath(tCont, contPath, cfg, models, true); err != nil {
		t.Fatal(err)
	}
	if !IsConnectedAtPath("continue", contPath) {
		t.Fatalf("expected continue connected")
	}
	if d, err := DisconnectAtPath(tCont, contPath); err != nil || !d.Removed || IsConnectedAtPath("continue", contPath) {
		t.Fatalf("continue disconnect failed: %+v %v", d, err)
	}
	contRaw, _ := os.ReadFile(contPath)
	if !strings.Contains(string(contRaw), "name: My Config") {
		t.Fatalf("expected user continue config preserved, got: %s", contRaw)
	}

	// 9. Disconnect T3 Code, Zed, Codex, Claude, OpenCode, Pi preserves all other keys
	if d, err := DisconnectAtPath(tT3, t3Path); err != nil || !d.Removed || IsConnectedAtPath("t3code", t3Path) {
		t.Fatalf("t3code disconnect failed: %+v %v", d, err)
	}
	if d, err := DisconnectAtPath(tZed, zedPath); err != nil || !d.Removed || IsConnectedAtPath("zed", zedPath) {
		t.Fatalf("zed disconnect failed: %+v %v", d, err)
	}
	if d, err := DisconnectAtPath(tCodex, codexPath); err != nil || !d.Removed || IsConnectedAtPath("codex", codexPath) {
		t.Fatalf("codex disconnect failed: %+v %v", d, err)
	}
	if d, err := DisconnectAtPath(tCl, clPath); err != nil || !d.Removed || IsConnectedAtPath("claude", clPath) {
		t.Fatalf("claude disconnect failed: %+v %v", d, err)
	}
	if d, err := DisconnectAtPath(tOC, ocPath); err != nil || !d.Removed || IsConnectedAtPath("opencode", ocPath) {
		t.Fatalf("opencode disconnect failed: %+v %v", d, err)
	}
	if d, err := DisconnectAtPath(tPi, piPath); err != nil || !d.Removed || IsConnectedAtPath("pi", piPath) {
		t.Fatalf("pi disconnect failed: %+v %v", d, err)
	}
}
