package connect

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/router"
)

type Target struct {
	ID          string
	Name        string
	Description string
	ConfigPath  string
	AutoApply   bool // true if apx can safely write/merge its config file directly
	Detected    bool
	Connected   bool // true if apx is currently registered in ConfigPath
}

type Result struct {
	Target  Target
	Applied bool
	Removed bool
	Path    string
	Backup  string
	Snippet string
	Message string
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func apxBinaryPath() string {
	if p, err := exec.LookPath("apx"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "apx"
}

func vscodeUserDir(appName string) string {
	home := homeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", appName, "User")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, appName, "User")
		}
		return filepath.Join(home, "AppData", "Roaming", appName, "User")
	default:
		return filepath.Join(home, ".config", appName, "User")
	}
}

func zedSettingsPath() string {
	home := homeDir()
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Zed", "settings.json")
		}
		return filepath.Join(home, "AppData", "Roaming", "Zed", "settings.json")
	}
	return filepath.Join(home, ".config", "zed", "settings.json")
}

func deltaDataDir() string {
	home := homeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Delta")
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "Delta")
		}
		return filepath.Join(home, "AppData", "Local", "Delta")
	default:
		if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
			return filepath.Join(xdgData, "delta")
		}
		return filepath.Join(home, ".local", "share", "delta")
	}
}

func detectJetBrains(home string) bool {
	if _, err := os.Stat(filepath.Join(home, ".jetbrains")); err == nil {
		return true
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(filepath.Join(home, "Library", "Application Support", "JetBrains")); err == nil {
			return true
		}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			if _, err := os.Stat(filepath.Join(appData, "JetBrains")); err == nil {
				return true
			}
		}
	default:
		if _, err := os.Stat(filepath.Join(home, ".config", "JetBrains")); err == nil {
			return true
		}
	}
	for _, bin := range []string{"idea", "rider", "pycharm", "webstorm", "goland", "rustrover", "clion", "phpstorm"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// ListTargets returns all supported IDEs, editors, and coding CLIs that apx can connect/disconnect.
func ListTargets() []Target {
	home := homeDir()
	exists := func(p string) bool {
		if p == "" {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}
	hasBin := func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	}

	codeDir := vscodeUserDir("Code")
	insidersDir := vscodeUserDir("Code - Insiders")
	codiumDir := vscodeUserDir("VSCodium")
	zedPath := zedSettingsPath()

	targets := []Target{
		{
			ID:          "t3code",
			Name:        "T3 Code",
			Description: "Native ACP agent (`apx acp`) in ~/.t3/userdata/settings.json",
			ConfigPath:  filepath.Join(home, ".t3", "userdata", "settings.json"),
			AutoApply:   true,
			Detected:    exists(filepath.Join(home, ".t3")),
		},
		{
			ID:          "zed",
			Name:        "Zed",
			Description: "Native ACP custom agent_server (`apx acp`) in Zed settings.json",
			ConfigPath:  zedPath,
			AutoApply:   true,
			Detected:    hasBin("zed") || hasBin("zeditor") || exists(filepath.Dir(zedPath)),
		},
		{
			ID:          "delta",
			Name:        "Delta (Zed Agent IDE)",
			Description: "Native ACP custom agent (`apx acp`) or OpenAI-compatible provider for Zed Delta",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("delta") || exists(deltaDataDir()),
		},
		{
			ID:          "vscode",
			Name:        "VS Code",
			Description: "Copilot BYOK OpenAI-compatible gateway in chatLanguageModels.json",
			ConfigPath:  filepath.Join(codeDir, "chatLanguageModels.json"),
			AutoApply:   true,
			Detected:    hasBin("code") || exists(codeDir),
		},
		{
			ID:          "vscode-insiders",
			Name:        "VS Code Insiders",
			Description: "Copilot BYOK OpenAI-compatible gateway in Code Insiders chatLanguageModels.json",
			ConfigPath:  filepath.Join(insidersDir, "chatLanguageModels.json"),
			AutoApply:   true,
			Detected:    hasBin("code-insiders") || exists(insidersDir),
		},
		{
			ID:          "vscodium",
			Name:        "VSCodium",
			Description: "OpenAI-compatible gateway in VSCodium chatLanguageModels.json",
			ConfigPath:  filepath.Join(codiumDir, "chatLanguageModels.json"),
			AutoApply:   true,
			Detected:    hasBin("codium") || exists(codiumDir),
		},
		{
			ID:          "jetbrains",
			Name:        "JetBrains IDEs",
			Description: "Native ACP agent (`apx acp`) in ~/.jetbrains/acp.json (IntelliJ, Rider, PyCharm, GoLand...)",
			ConfigPath:  filepath.Join(home, ".jetbrains", "acp.json"),
			AutoApply:   true,
			Detected:    detectJetBrains(home),
		},
		{
			ID:          "continue",
			Name:        "Continue.dev",
			Description: "OpenAI-compatible models in ~/.continue/config.yaml (VS Code & JetBrains)",
			ConfigPath:  filepath.Join(home, ".continue", "config.yaml"),
			AutoApply:   true,
			Detected:    exists(filepath.Join(home, ".continue")),
		},
		{
			ID:          "neovim",
			Name:        "Neovim",
			Description: "ACP + OpenAI Lua helper module at ~/.config/nvim/apx.lua (codecompanion/avante)",
			ConfigPath:  filepath.Join(home, ".config", "nvim", "apx.lua"),
			AutoApply:   true,
			Detected:    hasBin("nvim") || exists(filepath.Join(home, ".config", "nvim")),
		},
		{
			ID:          "opencode",
			Name:        "OpenCode",
			Description: "OpenAI-compatible `apx` provider in ~/.config/opencode/opencode.json",
			ConfigPath:  filepath.Join(home, ".config", "opencode", "opencode.json"),
			AutoApply:   true,
			Detected:    hasBin("opencode") || exists(filepath.Join(home, ".config", "opencode")),
		},
		{
			ID:          "codex",
			Name:        "Codex CLI",
			Description: "`[model_providers.apx]` block in ~/.codex/config.toml",
			ConfigPath:  filepath.Join(home, ".codex", "config.toml"),
			AutoApply:   true,
			Detected:    hasBin("codex") || exists(filepath.Join(home, ".codex")),
		},
		{
			ID:          "claude",
			Name:        "Claude Code",
			Description: "ANTHROPIC_BASE_URL & ANTHROPIC_AUTH_TOKEN in ~/.claude/settings.json",
			ConfigPath:  filepath.Join(home, ".claude", "settings.json"),
			AutoApply:   true,
			Detected:    hasBin("claude") || exists(filepath.Join(home, ".claude")),
		},
		{
			ID:          "pi",
			Name:        "Pi Coding Agent",
			Description: "`apx` provider in ~/.pi/agent/models.json",
			ConfigPath:  filepath.Join(home, ".pi", "agent", "models.json"),
			AutoApply:   true,
			Detected:    hasBin("pi") || exists(filepath.Join(home, ".pi")),
		},
		{
			ID:          "grok",
			Name:        "Grok CLI",
			Description: "baseURL & apiKey in ~/.grok/user-settings.json",
			ConfigPath:  filepath.Join(home, ".grok", "user-settings.json"),
			AutoApply:   true,
			Detected:    hasBin("grok") || exists(filepath.Join(home, ".grok")),
		},
		{
			ID:          "aider",
			Name:        "Aider",
			Description: "OpenAI base URL & API key block in ~/.aider.conf.yml",
			ConfigPath:  filepath.Join(home, ".aider.conf.yml"),
			AutoApply:   true,
			Detected:    hasBin("aider") || exists(filepath.Join(home, ".aider.conf.yml")),
		},
		{
			ID:          "cursor",
			Name:        "Cursor",
			Description: "OpenAI Base URL override + API key for Cursor Settings",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("cursor") || hasBin("agent") || exists(vscodeUserDir("Cursor")),
		},
		{
			ID:          "windsurf",
			Name:        "Windsurf",
			Description: "OpenAI-compatible endpoint + API key for Windsurf Settings",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("windsurf") || exists(vscodeUserDir("Windsurf")),
		},
		{
			ID:          "cline",
			Name:        "Cline / Roo / Kilo",
			Description: "OpenAI-compatible provider settings for Cline, Roo Code & Kilo Code",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("cline"),
		},
		{
			ID:          "agy",
			Name:        "Shell / Env",
			Description: "Shell exports (OPENAI_BASE_URL / ANTHROPIC_BASE_URL) & `apx acp`",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    true,
		},
	}

	for i := range targets {
		if targets[i].AutoApply && targets[i].ConfigPath != "" {
			targets[i].Connected = IsConnectedAtPath(targets[i].ID, targets[i].ConfigPath)
		}
	}
	return targets
}

// targetAliases allows convenient shorthand names (e.g. "code" -> "vscode", "rider" -> "jetbrains", "nvim" -> "neovim").
var targetAliases = map[string]string{
	"code":          "vscode",
	"code-insiders": "vscode-insiders",
	"insiders":      "vscode-insiders",
	"codium":        "vscodium",
	"jb":            "jetbrains",
	"intellij":      "jetbrains",
	"idea":          "jetbrains",
	"rider":         "jetbrains",
	"pycharm":       "jetbrains",
	"webstorm":      "jetbrains",
	"goland":        "jetbrains",
	"rustrover":     "jetbrains",
	"clion":         "jetbrains",
	"nvim":          "neovim",
	"roo":           "cline",
	"roocode":       "cline",
	"kilo":          "cline",
	"t3":            "t3code",
	"zed-delta":     "delta",
	"shell":         "agy",
}

func FindTarget(id string) (Target, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if alias, ok := targetAliases[id]; ok {
		id = alias
	}
	for _, t := range ListTargets() {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}

// IsConnectedAtPath reports whether apx is currently registered in the given target config file.
func IsConnectedAtPath(targetID, path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	switch targetID {
	case "t3code":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			if provs, ok := root["providerInstances"].(map[string]any); ok {
				_, exists := provs["apx"]
				return exists
			}
		}
	case "zed", "jetbrains":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			if servers, ok := root["agent_servers"].(map[string]any); ok {
				_, exists := servers["apx"]
				return exists
			}
		}
	case "vscode", "vscode-insiders", "vscodium":
		var arr []map[string]any
		if json.Unmarshal(stripJSONC(raw), &arr) == nil {
			for _, item := range arr {
				if name, _ := item["name"].(string); strings.EqualFold(name, "apx") {
					return true
				}
			}
		}
	case "opencode":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			if prov, ok := root["provider"].(map[string]any); ok {
				_, exists := prov["apx"]
				return exists
			}
		}
	case "pi":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			if provs, ok := root["providers"].(map[string]any); ok {
				_, exists := provs["apx"]
				return exists
			}
		}
	case "claude":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			if env, ok := root["env"].(map[string]any); ok {
				tok, _ := env["ANTHROPIC_AUTH_TOKEN"].(string)
				key, _ := env["ANTHROPIC_API_KEY"].(string)
				return strings.HasPrefix(tok, "apx_") || strings.HasPrefix(key, "apx_")
			}
		}
	case "grok":
		var root map[string]any
		if json.Unmarshal(stripJSONC(raw), &root) == nil {
			key, _ := root["apiKey"].(string)
			return strings.HasPrefix(key, "apx_")
		}
	case "codex", "continue", "aider":
		return strings.Contains(string(raw), "# --- apx begin ---")
	case "neovim":
		return strings.Contains(string(raw), "apx")
	}
	return false
}

// Apply registers apx into the target client's config file (if apply==true and supported)
// and always returns a clean, concise human-readable snippet.
func Apply(targetID string, cfg *config.Config, models []router.ModelEntry, apply bool) (*Result, error) {
	t, ok := FindTarget(targetID)
	if !ok {
		return nil, fmt.Errorf("unknown client %q", targetID)
	}
	return ApplyAtPath(t, t.ConfigPath, cfg, models, apply)
}

// ApplyAtPath is like Apply with an explicit config path (useful for tests).
func ApplyAtPath(t Target, path string, cfg *config.Config, models []router.ModelEntry, apply bool) (*Result, error) {
	baseURL := cfg.BaseURL()
	anthURL := strings.TrimSuffix(baseURL, "/v1")
	key := cfg.Server.LocalKey
	apxBin := apxBinaryPath()

	modelIDs := make([]string, 0, len(models))
	excludedSelf := 0
	for _, m := range models {
		// Loop prevention: never register a client's own backend models back into that same client.
		if t.ID != "t3code" && t.ID != "zed" && t.ID != "jetbrains" &&
			t.ID != "vscode" && t.ID != "vscode-insiders" && t.ID != "vscodium" &&
			t.ID != "continue" && t.ID != "neovim" && t.ID != "aider" &&
			m.Backend != "" && m.Backend == t.ID {
			excludedSelf++
			continue
		}
		modelIDs = append(modelIDs, m.ID)
	}
	enabledCount := len(modelIDs)
	effectiveModels := modelIDs
	if len(effectiveModels) == 0 {
		effectiveModels = []string{"default"}
	}
	loopNote := ""
	if excludedSelf > 0 {
		loopNote = fmt.Sprintf(" (loop guard: excluded %d self '%s' model(s))", excludedSelf, t.ID)
	}

	res := &Result{Target: t, Path: path}

	switch t.ID {
	case "t3code":
		res.Snippet = fmt.Sprintf(
			"T3 Code — Native ACP Agent (Settings -> Providers -> Custom ACP Agent):\n"+
				"    Name:         AgentPassage\n"+
				"    Command Path: %s\n"+
				"    Command Args: acp\n"+
				"    Models:       %d enabled (auto-discovered via ACP)\n\n"+
				"OpenAI-compatible endpoint (optional):\n"+
				"    Base URL: %s\n"+
				"    API Key:  %s\n",
			apxBin, enabledCount, baseURL, key,
		)
		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				provs, _ := root["providerInstances"].(map[string]any)
				if provs == nil {
					provs = make(map[string]any)
				}
				delete(provs, "acpx")
				existing, _ := provs["apx"].(map[string]any)
				displayName := "AgentPassage"
				accentColor := "#0050ff"
				if existing != nil {
					if dn, ok := existing["displayName"].(string); ok && dn != "" {
						displayName = dn
					}
					if ac, ok := existing["accentColor"].(string); ok && ac != "" {
						accentColor = ac
					}
				}
				provs["apx"] = map[string]any{
					"driver":      "acpRegistry",
					"displayName": displayName,
					"accentColor": accentColor,
					"enabled":     true,
					"config": map[string]any{
						"agentId":     "apx",
						"source":      "local",
						"commandPath": apxBin,
						"commandArgs": []string{"acp"},
					},
				}
				root["providerInstances"] = provs
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered AgentPassage (%d models) in %s — reload T3 Code to activate", enabledCount, path)
		}

	case "zed":
		agentEntry := map[string]any{
			"type":    "custom",
			"command": apxBin,
			"args":    []string{"acp"},
		}
		snipRaw, _ := json.MarshalIndent(map[string]any{
			"agent_servers": map[string]any{
				"apx": agentEntry,
			},
		}, "", "  ")
		res.Snippet = string(snipRaw) + fmt.Sprintf("\n# %d models auto-discovered via ACP\n", enabledCount)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				servers, _ := root["agent_servers"].(map[string]any)
				if servers == nil {
					servers = make(map[string]any)
				}
				delete(servers, "acpx")
				servers["apx"] = agentEntry
				root["agent_servers"] = servers
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx ACP agent_server (%d models) in %s", enabledCount, path)
		}

	case "jetbrains":
		agentEntry := map[string]any{
			"type":    "custom",
			"command": apxBin,
			"args":    []string{"acp"},
		}
		snipRaw, _ := json.MarshalIndent(map[string]any{
			"agent_servers": map[string]any{
				"apx": agentEntry,
			},
		}, "", "  ")
		res.Snippet = string(snipRaw) + fmt.Sprintf("\n# JetBrains AI Chat -> %d models auto-discovered via ACP\n", enabledCount)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				servers, _ := root["agent_servers"].(map[string]any)
				if servers == nil {
					servers = make(map[string]any)
				}
				delete(servers, "acpx")
				servers["apx"] = agentEntry
				root["agent_servers"] = servers
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx in JetBrains %s (%d models) — reopen AI Chat to reload", path, enabledCount)
		}

	case "vscode", "vscode-insiders", "vscodium":
		vsModels := make([]map[string]any, 0, len(effectiveModels))
		for _, id := range effectiveModels {
			vsModels = append(vsModels, map[string]any{
				"id":     id,
				"name":   id,
				"url":    baseURL + "/chat/completions",
				"tokens": 128000,
				"capabilities": map[string]any{
					"toolCalling": true,
					"imageInput":  false,
				},
			})
		}
		vsEntry := map[string]any{
			"name":    "apx",
			"vendor":  "customendpoint",
			"apiKey":  key,
			"apiType": "chat-completions",
			"models":  vsModels,
		}
		res.Snippet = fmt.Sprintf(
			"%s — Copilot BYOK (chatLanguageModels.json):\n"+
				"  Endpoint: %s/chat/completions\n"+
				"  API Key:  %s\n"+
				"  Models:   %d enabled (open Chat -> Manage Language Models -> apx)\n",
			t.Name, baseURL, key, enabledCount,
		)

		if apply && path != "" {
			bak, err := mergeJSONArrayFile(path, func(items []map[string]any) []map[string]any {
				for i, item := range items {
					if name, _ := item["name"].(string); strings.EqualFold(name, "apx") {
						items[i] = vsEntry
						return items
					}
				}
				return append(items, vsEntry)
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx (%d models) in %s", enabledCount, path)
		}

	case "continue":
		var sb strings.Builder
		sb.WriteString("# --- apx begin ---\n")
		sb.WriteString("models:\n")
		for _, id := range effectiveModels {
			sb.WriteString(fmt.Sprintf("  - name: apx / %s\n", id))
			sb.WriteString("    provider: openai\n")
			sb.WriteString(fmt.Sprintf("    model: %s\n", id))
			sb.WriteString(fmt.Sprintf("    apiBase: %s\n", baseURL))
			sb.WriteString(fmt.Sprintf("    apiKey: %s\n", key))
			sb.WriteString("    roles:\n      - chat\n      - edit\n      - apply\n")
		}
		sb.WriteString("# --- apx end ---\n")
		res.Snippet = fmt.Sprintf(
			"Continue.dev (~/.continue/config.yaml):\n"+
				"  Provider: openai\n"+
				"  API Base: %s\n"+
				"  API Key:  %s\n"+
				"  Models:   %d enabled\n",
			baseURL, key, enabledCount,
		)
		if apply && path != "" {
			bak, err := upsertTextBlock(path, "# --- apx begin ---", "# --- apx end ---", sb.String())
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx (%d models) in %s", enabledCount, path)
		}

	case "neovim":
		lua := fmt.Sprintf(`-- Generated by apx (disconnect anytime with: apx disconnect neovim)
-- Works with codecompanion.nvim (native ACP or OpenAI) and avante.nvim
return {
  acp = {
    command = %q,
    args = { "acp" },
  },
  openai = {
    endpoint = %q,
    api_key = %q,
    model = %q,
  },
}
`, apxBin, baseURL, key, effectiveModels[0])
		res.Snippet = lua + fmt.Sprintf("-- %d models available via apx\n", enabledCount)
		if apply && path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, []byte(lua), 0o600); err != nil {
				return nil, err
			}
			res.Applied = true
			res.Message = fmt.Sprintf("Wrote Neovim helper module (%d models) to %s", enabledCount, path)
		}

	case "opencode":
		modelMap := make(map[string]any, len(effectiveModels))
		for _, id := range effectiveModels {
			modelMap[id] = map[string]any{"name": id}
		}
		providerObj := map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "apx",
			"options": map[string]any{
				"baseURL": baseURL,
				"apiKey":  key,
				"headers": map[string]string{
					"X-Apx-Client": "opencode",
				},
			},
			"models": modelMap,
		}
		res.Snippet = fmt.Sprintf(
			"OpenCode provider `apx`:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Header:   X-Apx-Client: opencode\n"+
				"  Models:   %d enabled%s\n",
			baseURL, key, enabledCount, loopNote,
		)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				if _, ok := root["$schema"]; !ok {
					root["$schema"] = "https://opencode.ai/config.json"
				}
				prov, _ := root["provider"].(map[string]any)
				if prov == nil {
					prov = make(map[string]any)
				}
				prov["apx"] = providerObj
				root["provider"] = prov
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx (%d models) in %s%s", enabledCount, path, loopNote)
		}

	case "codex":
		block := fmt.Sprintf(
			"# --- apx begin ---\n"+
				"[model_providers.apx]\n"+
				"name = \"apx\"\n"+
				"base_url = %q\n"+
				"env_key = \"APX_API_KEY\"\n"+
				"wire_api = \"chat\"\n"+
				"# --- apx end ---\n",
			baseURL,
		)
		res.Snippet = block + fmt.Sprintf("\n# Run Codex with apx (%d models):\nexport APX_API_KEY=%q\ncodex -c model_provider=apx -m %s\n", enabledCount, key, effectiveModels[0])

		if apply && path != "" {
			bak, err := upsertTextBlock(path, "# --- apx begin ---", "# --- apx end ---", block)
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Registered [model_providers.apx] in " + path
		}

	case "claude":
		envObj := map[string]any{
			"ANTHROPIC_BASE_URL":   anthURL,
			"ANTHROPIC_AUTH_TOKEN": key,
			"ANTHROPIC_API_KEY":    key,
		}
		snipRaw, _ := json.MarshalIndent(map[string]any{"env": envObj}, "", "  ")
		res.Snippet = string(snipRaw) + fmt.Sprintf("\n# %d models available\n", enabledCount)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				env, _ := root["env"].(map[string]any)
				if env == nil {
					env = make(map[string]any)
				}
				for k, v := range envObj {
					env[k] = v
				}
				root["env"] = env
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Configured Claude Code settings in " + path
		}

	case "pi":
		piModels := make([]map[string]any, 0, len(effectiveModels))
		for _, id := range effectiveModels {
			piModels = append(piModels, map[string]any{
				"id":   id,
				"name": id,
			})
		}
		piProv := map[string]any{
			"baseUrl": baseURL,
			"apiKey":  key,
			"api":     "openai-completions",
			"headers": map[string]string{
				"X-Apx-Client": "pi",
			},
			"models": piModels,
		}
		res.Snippet = fmt.Sprintf(
			"Pi Coding Agent provider `apx`:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Header:   X-Apx-Client: pi\n"+
				"  Models:   %d enabled%s\n",
			baseURL, key, enabledCount, loopNote,
		)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				provs, _ := root["providers"].(map[string]any)
				if provs == nil {
					provs = make(map[string]any)
				}
				provs["apx"] = piProv
				root["providers"] = provs
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered apx (%d models) in %s%s", enabledCount, path, loopNote)
		}

	case "grok":
		snipRaw, _ := json.MarshalIndent(map[string]any{
			"baseURL": baseURL,
			"apiKey":  key,
			"model":   effectiveModels[0],
		}, "", "  ")
		res.Snippet = string(snipRaw) + fmt.Sprintf("\n# %d models available\n", enabledCount)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				root["baseURL"] = baseURL
				root["apiKey"] = key
				if _, ok := root["model"]; !ok {
					root["model"] = effectiveModels[0]
				}
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Configured Grok CLI in " + path
		}

	case "aider":
		block := fmt.Sprintf(
			"# --- apx begin ---\n"+
				"openai-api-base: %s\n"+
				"openai-api-key: %s\n"+
				"model: openai/%s\n"+
				"# --- apx end ---\n",
			baseURL, key, effectiveModels[0],
		)
		res.Snippet = block + fmt.Sprintf("# %d models available\n", enabledCount)
		if apply && path != "" {
			bak, err := upsertTextBlock(path, "# --- apx begin ---", "# --- apx end ---", block)
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Configured Aider in " + path
		}

	case "delta":
		res.Snippet = fmt.Sprintf(
			"Delta (Zed Agent IDE) -> Settings -> Custom ACP Agent:\n"+
				"  Name:    AgentPassage (apx)\n"+
				"  Command: %s\n"+
				"  Args:    acp\n"+
				"  Models:  %d enabled (auto-discovered via ACP)\n\n"+
				"Or connect via OpenAI-compatible provider:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n",
			apxBin, enabledCount, baseURL, key,
		)
		res.Message = "Copy Delta (Zed Agent IDE) settings above (Delta encrypts its settings.json)."

	case "cursor":
		res.Snippet = fmt.Sprintf(
			"Cursor Settings -> Models -> OpenAI API Key:\n"+
				"  Override OpenAI Base URL: %s\n"+
				"  OpenAI API Key:           %s\n"+
				"  Models:                   %d enabled\n",
			baseURL, key, enabledCount,
		)
		res.Message = "Copy Cursor settings above (Cursor stores settings in its GUI)."

	case "windsurf":
		res.Snippet = fmt.Sprintf(
			"Windsurf Settings -> Custom OpenAI-compatible Provider:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Models:   %d enabled\n",
			baseURL, key, enabledCount,
		)
		res.Message = "Copy Windsurf settings above."

	case "cline":
		res.Snippet = fmt.Sprintf(
			"Cline / Roo Code / Kilo Code -> API Provider -> OpenAI Compatible:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Models:   %d enabled (default: %s)\n",
			baseURL, key, enabledCount, effectiveModels[0],
		)
		res.Message = "Copy Cline / Roo / Kilo settings above."

	default:
		res.Snippet = fmt.Sprintf(
			"export OPENAI_BASE_URL=%q\n"+
				"export OPENAI_API_KEY=%q\n"+
				"export ANTHROPIC_BASE_URL=%q\n"+
				"export ANTHROPIC_API_KEY=%q\n"+
				"# Native ACP stdio server (%d models):\n"+
				"# %s acp\n",
			baseURL, key, anthURL, key, enabledCount, apxBin,
		)
		res.Message = "Shell exports ready."
	}

	return res, nil
}

// Disconnect cleanly removes apx's registration from a target client without touching other settings.
func Disconnect(targetID string) (*Result, error) {
	t, ok := FindTarget(targetID)
	if !ok {
		return nil, fmt.Errorf("unknown client %q", targetID)
	}
	return DisconnectAtPath(t, t.ConfigPath)
}

// DisconnectAll cleanly removes apx from every auto-configured IDE & CLI where it is currently registered.
func DisconnectAll() ([]Result, []error) {
	var results []Result
	var errs []error
	for _, t := range ListTargets() {
		if !t.AutoApply || t.ConfigPath == "" || !t.Connected {
			continue
		}
		res, err := DisconnectAtPath(t, t.ConfigPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.ID, err))
			continue
		}
		if res != nil {
			results = append(results, *res)
		}
	}
	return results, errs
}

// DisconnectAtPath removes apx's registration from the specified config path (useful for tests).
func DisconnectAtPath(t Target, path string) (*Result, error) {
	res := &Result{Target: t, Path: path}
	if !t.AutoApply || path == "" {
		res.Message = fmt.Sprintf("%s is configured manually in its GUI — remove the apx Base URL there.", t.Name)
		return res, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		res.Message = fmt.Sprintf("%s is not connected (%s not found)", t.Name, path)
		return res, nil
	}

	switch t.ID {
	case "t3code":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if provs, ok := root["providerInstances"].(map[string]any); ok {
				if _, exists := provs["apx"]; exists {
					delete(provs, "apx")
					removed = true
				}
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "zed", "jetbrains":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if servers, ok := root["agent_servers"].(map[string]any); ok {
				if _, exists := servers["apx"]; exists {
					delete(servers, "apx")
					removed = true
				}
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "vscode", "vscode-insiders", "vscodium":
		removed := false
		bak, err := mergeJSONArrayFile(path, func(items []map[string]any) []map[string]any {
			kept := make([]map[string]any, 0, len(items))
			for _, item := range items {
				if name, _ := item["name"].(string); strings.EqualFold(name, "apx") {
					removed = true
					continue
				}
				kept = append(kept, item)
			}
			return kept
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "opencode":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if prov, ok := root["provider"].(map[string]any); ok {
				if _, exists := prov["apx"]; exists {
					delete(prov, "apx")
					removed = true
				}
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "pi":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if provs, ok := root["providers"].(map[string]any); ok {
				if _, exists := provs["apx"]; exists {
					delete(provs, "apx")
					removed = true
				}
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "claude":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if env, ok := root["env"].(map[string]any); ok {
				tok, _ := env["ANTHROPIC_AUTH_TOKEN"].(string)
				key, _ := env["ANTHROPIC_API_KEY"].(string)
				base, _ := env["ANTHROPIC_BASE_URL"].(string)
				if strings.HasPrefix(tok, "apx_") || strings.HasPrefix(key, "apx_") || strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") {
					if strings.HasPrefix(tok, "apx_") {
						delete(env, "ANTHROPIC_AUTH_TOKEN")
						removed = true
					}
					if strings.HasPrefix(key, "apx_") {
						delete(env, "ANTHROPIC_API_KEY")
						removed = true
					}
					if strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") {
						delete(env, "ANTHROPIC_BASE_URL")
						removed = true
					}
				}
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "grok":
		removed := false
		bak, err := mergeJSONFile(path, func(root map[string]any) {
			if key, _ := root["apiKey"].(string); strings.HasPrefix(key, "apx_") {
				delete(root, "apiKey")
				delete(root, "baseURL")
				removed = true
			}
		})
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "codex", "continue", "aider":
		removed, bak, err := removeTextBlock(path, "# --- apx begin ---", "# --- apx end ---")
		if err != nil {
			return nil, err
		}
		res.Removed = removed
		res.Backup = bak

	case "neovim":
		if err := os.Remove(path); err == nil {
			res.Removed = true
		}
	}

	if res.Removed {
		res.Message = fmt.Sprintf("Disconnected apx from %s (%s)", t.Name, path)
	} else {
		res.Message = fmt.Sprintf("No apx entry found in %s (%s)", t.Name, path)
	}
	return res, nil
}

func mergeJSONFile(path string, mutate func(map[string]any)) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	root := make(map[string]any)
	var backupPath string
	if existing, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(existing))) > 0 {
		clean := stripJSONC(existing)
		if err := json.Unmarshal(clean, &root); err != nil {
			return "", fmt.Errorf("existing %s is not valid JSON: %w", path, err)
		}
		backupPath = path + ".apx.bak"
		_ = os.WriteFile(backupPath, existing, 0o600)
	}
	mutate(root)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return backupPath, nil
}

func mergeJSONArrayFile(path string, mutate func([]map[string]any) []map[string]any) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var items []map[string]any
	var backupPath string
	if existing, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(existing))) > 0 {
		clean := stripJSONC(existing)
		if err := json.Unmarshal(clean, &items); err != nil {
			return "", fmt.Errorf("existing %s is not a valid JSON array: %w", path, err)
		}
		backupPath = path + ".apx.bak"
		_ = os.WriteFile(backupPath, existing, 0o600)
	}
	items = mutate(items)
	if items == nil {
		items = []map[string]any{}
	}
	out, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return backupPath, nil
}

// stripJSONC removes // and /* */ comments and trailing commas outside string literals
// so JSONC config files (e.g. Zed / VS Code settings) parse cleanly with encoding/json.
func stripJSONC(src []byte) []byte {
	var noComments []byte
	inStr := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			noComments = append(noComments, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			noComments = append(noComments, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				noComments = append(noComments, '\n')
			}
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		noComments = append(noComments, c)
	}

	out := make([]byte, 0, len(noComments))
	inStr = false
	escaped = false
	for i := 0; i < len(noComments); i++ {
		c := noComments[i]
		if inStr {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(noComments) && (noComments[j] == ' ' || noComments[j] == '\t' || noComments[j] == '\n' || noComments[j] == '\r') {
				j++
			}
			if j < len(noComments) && (noComments[j] == '}' || noComments[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func upsertTextBlock(path, startMarker, endMarker, block string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var existing string
	var backupPath string
	if raw, err := os.ReadFile(path); err == nil {
		existing = string(raw)
		backupPath = path + ".apx.bak"
		_ = os.WriteFile(backupPath, raw, 0o600)
	}

	var updated string
	if sIdx := strings.Index(existing, startMarker); sIdx >= 0 {
		if eIdx := strings.Index(existing[sIdx:], endMarker); eIdx >= 0 {
			endPos := sIdx + eIdx + len(endMarker)
			if endPos < len(existing) && existing[endPos] == '\n' {
				endPos++
			}
			updated = existing[:sIdx] + block + existing[endPos:]
		}
	}
	if updated == "" {
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		if existing != "" {
			existing += "\n"
		}
		updated = existing + block
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return backupPath, nil
}

func removeTextBlock(path, startMarker, endMarker string) (bool, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, "", err
	}
	existing := string(raw)
	sIdx := strings.Index(existing, startMarker)
	if sIdx < 0 {
		return false, "", nil
	}
	eIdx := strings.Index(existing[sIdx:], endMarker)
	if eIdx < 0 {
		return false, "", nil
	}
	backupPath := path + ".apx.bak"
	_ = os.WriteFile(backupPath, raw, 0o600)

	endPos := sIdx + eIdx + len(endMarker)
	if endPos < len(existing) && existing[endPos] == '\n' {
		endPos++
	}
	// Trim one extra leading blank line if present.
	if sIdx > 0 && existing[sIdx-1] == '\n' && (sIdx == 1 || existing[sIdx-2] == '\n') {
		sIdx--
	}
	updated := existing[:sIdx] + existing[endPos:]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o600); err != nil {
		return false, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, "", err
	}
	return true, backupPath, nil
}
