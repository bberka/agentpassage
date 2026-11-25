package connect

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/router"
)

type Target struct {
	ID          string
	Name        string
	Description string
	ConfigPath  string
	AutoApply   bool // true if acpx can safely write/merge its config file directly
	Detected    bool
}

type Result struct {
	Target   Target
	Applied  bool
	Path     string
	Backup   string
	Snippet  string
	Message  string
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func acpxBinaryPath() string {
	if p, err := exec.LookPath("acpx"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "acpx"
}

// ListTargets returns all supported client CLIs and IDEs that acpx can register itself into.
func ListTargets() []Target {
	home := homeDir()
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}
	hasBin := func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	}

	return []Target{
		{
			ID:          "t3code",
			Name:        "T3 Code",
			Description: "Register acpx as a native ACP agent (`acpx acp`) in ~/.t3/userdata/settings.json",
			ConfigPath:  filepath.Join(home, ".t3", "userdata", "settings.json"),
			AutoApply:   true,
			Detected:    exists(filepath.Join(home, ".t3")),
		},
		{
			ID:          "zed",
			Name:        "Zed",
			Description: "Register acpx as a native ACP agent_server (`acpx acp`) in ~/.config/zed/settings.json",
			ConfigPath:  filepath.Join(home, ".config", "zed", "settings.json"),
			AutoApply:   true,
			Detected:    hasBin("zed") || hasBin("zeditor") || exists(filepath.Join(home, ".config", "zed")),
		},
		{
			ID:          "opencode",
			Name:        "OpenCode",
			Description: "Add/merge `acpx` OpenAI-compatible provider with all your models into opencode.json",
			ConfigPath:  filepath.Join(home, ".config", "opencode", "opencode.json"),
			AutoApply:   true,
			Detected:    hasBin("opencode") || exists(filepath.Join(home, ".config", "opencode")),
		},
		{
			ID:          "codex",
			Name:        "Codex CLI",
			Description: "Append/update `[model_providers.acpx]` in ~/.codex/config.toml",
			ConfigPath:  filepath.Join(home, ".codex", "config.toml"),
			AutoApply:   true,
			Detected:    hasBin("codex") || exists(filepath.Join(home, ".codex")),
		},
		{
			ID:          "claude",
			Name:        "Claude Code",
			Description: "Configure ANTHROPIC_BASE_URL & ANTHROPIC_AUTH_TOKEN in ~/.claude/settings.json",
			ConfigPath:  filepath.Join(home, ".claude", "settings.json"),
			AutoApply:   true,
			Detected:    hasBin("claude") || exists(filepath.Join(home, ".claude")),
		},
		{
			ID:          "pi",
			Name:        "Pi Coding Agent",
			Description: "Add/merge `acpx` provider & models in ~/.pi/agent/models.json",
			ConfigPath:  filepath.Join(home, ".pi", "agent", "models.json"),
			AutoApply:   true,
			Detected:    hasBin("pi") || exists(filepath.Join(home, ".pi")),
		},
		{
			ID:          "grok",
			Name:        "Grok CLI",
			Description: "Configure baseURL & apiKey in ~/.grok/user-settings.json",
			ConfigPath:  filepath.Join(home, ".grok", "user-settings.json"),
			AutoApply:   true,
			Detected:    hasBin("grok") || exists(filepath.Join(home, ".grok")),
		},
		{
			ID:          "cline",
			Name:        "Cline",
			Description: "Generate OpenAI-compatible provider settings for Cline (VS Code & CLI)",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("cline"),
		},
		{
			ID:          "cursor",
			Name:        "Cursor",
			Description: "Generate OpenAI Base URL override + API key + custom models for Cursor Settings",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("cursor") || hasBin("agent"),
		},
		{
			ID:          "agy",
			Name:        "Antigravity / Muse Code",
			Description: "Shell exports (OPENAI_BASE_URL / ANTHROPIC_BASE_URL) & ACP bridge command",
			ConfigPath:  "",
			AutoApply:   false,
			Detected:    hasBin("agy") || hasBin("muse-code"),
		},
	}
}

func FindTarget(id string) (Target, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, t := range ListTargets() {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}

// Apply registers acpx into the target client's config file (if apply==true and supported)
// and always returns a clean human-readable snippet.
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
	acpxBin := acpxBinaryPath()

	modelIDs := make([]string, 0, len(models))
	excludedSelf := 0
	for _, m := range models {
		// Loop prevention: never register a client's own backend models back into that same client
		// (e.g. `acpx connect pi` excludes `pi` backend models so pi cannot recurse into acpx -> pi).
		if t.ID != "t3code" && m.Backend != "" && m.Backend == t.ID {
			excludedSelf++
			continue
		}
		modelIDs = append(modelIDs, m.ID)
	}
	if len(modelIDs) == 0 {
		modelIDs = []string{"default"}
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
				"    Name:         ACPX\n"+
				"    Command Path: %s\n"+
				"    Command Args: acp\n"+
				"    Models:       %d enabled (auto-discovered via ACP)\n\n"+
				"OpenAI-compatible endpoint (optional):\n"+
				"    Base URL: %s\n"+
				"    API Key:  %s\n",
			acpxBin, len(modelIDs), baseURL, key,
		)
		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				provs, _ := root["providerInstances"].(map[string]any)
				if provs == nil {
					provs = make(map[string]any)
				}
				existing, _ := provs["acpx"].(map[string]any)
				displayName := "ACPX"
				accentColor := "#0050ff"
				if existing != nil {
					if dn, ok := existing["displayName"].(string); ok && dn != "" {
						displayName = dn
					}
					if ac, ok := existing["accentColor"].(string); ok && ac != "" {
						accentColor = ac
					}
				}
				provs["acpx"] = map[string]any{
					"driver":      "acpRegistry",
					"displayName": displayName,
					"accentColor": accentColor,
					"enabled":     true,
					"config": map[string]any{
						"agentId":     "acpx",
						"source":      "local",
						"commandPath": acpxBin,
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
			res.Message = fmt.Sprintf("Registered ACPX provider (%s + [acp]) in %s — reload T3 Code to activate", acpxBin, path)
		}

	case "zed":
		agentEntry := map[string]any{
			"command": acpxBin,
			"args":    []string{"acp"},
		}
		snipRaw, _ := json.MarshalIndent(map[string]any{
			"agent_servers": map[string]any{
				"acpx": agentEntry,
			},
		}, "", "  ")
		res.Snippet = string(snipRaw) + fmt.Sprintf("\n# %d models auto-discovered via ACP\n", len(modelIDs))

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				servers, _ := root["agent_servers"].(map[string]any)
				if servers == nil {
					servers = make(map[string]any)
				}
				servers["acpx"] = agentEntry
				root["agent_servers"] = servers
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered acpx ACP agent_server in %s (%d models auto-discovered)", path, len(modelIDs))
		}

	case "opencode":
		modelMap := make(map[string]any, len(modelIDs))
		for _, id := range modelIDs {
			modelMap[id] = map[string]any{"name": id}
		}
		providerObj := map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "acpx",
			"options": map[string]any{
				"baseURL": baseURL,
				"apiKey":  key,
				"headers": map[string]string{
					"X-Acpx-Client": "opencode",
				},
			},
			"models": modelMap,
		}
		res.Snippet = fmt.Sprintf(
			"OpenCode provider `acpx`:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Header:   X-Acpx-Client: opencode\n"+
				"  Models:   %d configured%s (run `acpx list` to view)\n",
			baseURL, key, len(modelIDs), loopNote,
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
				prov["acpx"] = providerObj
				root["provider"] = prov
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered acpx (%d models) in %s%s", len(modelIDs), path, loopNote)
		}

	case "codex":
		block := fmt.Sprintf(
			"# --- acpx begin ---\n"+
				"[model_providers.acpx]\n"+
				"name = \"acpx\"\n"+
				"base_url = %q\n"+
				"env_key = \"ACPX_API_KEY\"\n"+
				"wire_api = \"chat\"\n"+
				"# --- acpx end ---\n",
			baseURL,
		)
		res.Snippet = block + fmt.Sprintf("\n# Run Codex with acpx:\nexport ACPX_API_KEY=%q\ncodex -c model_provider=acpx -m %s\n", key, modelIDs[0])

		if apply && path != "" {
			bak, err := upsertTOMLBlock(path, "# --- acpx begin ---", "# --- acpx end ---", block)
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Registered [model_providers.acpx] in " + path
		}

	case "claude":
		envObj := map[string]any{
			"ANTHROPIC_BASE_URL":   anthURL,
			"ANTHROPIC_AUTH_TOKEN": key,
			"ANTHROPIC_API_KEY":    key,
		}
		snipRaw, _ := json.MarshalIndent(map[string]any{"env": envObj}, "", "  ")
		res.Snippet = string(snipRaw)

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
		piModels := make([]map[string]any, 0, len(modelIDs))
		for _, id := range modelIDs {
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
				"X-Acpx-Client": "pi",
			},
			"models": piModels,
		}
		res.Snippet = fmt.Sprintf(
			"Pi Coding Agent provider `acpx`:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Header:   X-Acpx-Client: pi\n"+
				"  Models:   %d configured%s (run `acpx list` to view)\n",
			baseURL, key, len(modelIDs), loopNote,
		)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				provs, _ := root["providers"].(map[string]any)
				if provs == nil {
					provs = make(map[string]any)
				}
				provs["acpx"] = piProv
				root["providers"] = provs
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = fmt.Sprintf("Registered acpx (%d models) in %s%s", len(modelIDs), path, loopNote)
		}

	case "grok":
		snipRaw, _ := json.MarshalIndent(map[string]any{
			"baseURL": baseURL,
			"apiKey":  key,
			"model":   modelIDs[0],
		}, "", "  ")
		res.Snippet = string(snipRaw)

		if apply && path != "" {
			bak, err := mergeJSONFile(path, func(root map[string]any) {
				root["baseURL"] = baseURL
				root["apiKey"] = key
				if _, ok := root["model"]; !ok {
					root["model"] = modelIDs[0]
				}
			})
			if err != nil {
				return nil, err
			}
			res.Applied = true
			res.Backup = bak
			res.Message = "Configured Grok CLI in " + path
		}

	case "cursor":
		res.Snippet = fmt.Sprintf(
			"Cursor Settings -> Models -> OpenAI API Key:\n"+
				"  Override OpenAI Base URL: %s\n"+
				"  OpenAI API Key:           %s\n"+
				"  Models:                   %d enabled (run `acpx list` or GET %s/models)\n",
			baseURL, key, len(modelIDs), baseURL,
		)
		res.Message = "Copy Cursor settings above (Cursor stores settings in its GUI)."

	case "cline":
		res.Snippet = fmt.Sprintf(
			"Cline Settings -> API Provider -> OpenAI Compatible:\n"+
				"  Base URL: %s\n"+
				"  API Key:  %s\n"+
				"  Model ID: %s\n",
			baseURL, key, modelIDs[0],
		)
		res.Message = "Copy Cline settings above."

	default:
		res.Snippet = fmt.Sprintf(
			"export OPENAI_BASE_URL=%q\n"+
				"export OPENAI_API_KEY=%q\n"+
				"export ANTHROPIC_BASE_URL=%q\n"+
				"export ANTHROPIC_API_KEY=%q\n"+
				"# Or connect any ACP client via stdio:\n"+
				"# %s acp\n",
			baseURL, key, anthURL, key, acpxBin,
		)
		res.Message = "Shell exports ready."
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
		backupPath = path + ".acpx.bak"
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

// stripJSONC removes // and /* */ comments and trailing commas outside string literals
// so JSONC config files (e.g. Zed's settings.json) parse cleanly with encoding/json.
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

	// Second pass: strip trailing commas immediately before '}' or ']' (outside strings).
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

func upsertTOMLBlock(path, startMarker, endMarker, block string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var existing string
	var backupPath string
	if raw, err := os.ReadFile(path); err == nil {
		existing = string(raw)
		backupPath = path + ".acpx.bak"
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
