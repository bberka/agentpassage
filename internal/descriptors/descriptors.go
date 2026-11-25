package descriptors

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ACPDescriptor defines how to detect, launch, isolate, and authenticate an ACP CLI.
type ACPDescriptor struct {
	ID            string            `yaml:"id" json:"id"`
	Name          string            `yaml:"name" json:"name"`
	Detect        string            `yaml:"detect" json:"detect"`
	Command       []string          `yaml:"command" json:"command"`
	ProfileArgs   []string          `yaml:"profile_args,omitempty" json:"profile_args,omitempty"`
	ProfileEnv    map[string]string `yaml:"profile_env,omitempty" json:"profile_env,omitempty"`
	Login         []string          `yaml:"login,omitempty" json:"login,omitempty"`
	DefaultModels []string          `yaml:"default_models,omitempty" json:"default_models,omitempty"`
	TermsNote     string            `yaml:"terms_note,omitempty" json:"terms_note,omitempty"`
	BuiltIn       bool              `yaml:"-" json:"built_in"`
	SourcePath    string            `yaml:"-" json:"source_path,omitempty"`
}

type DetectedCLI struct {
	Descriptor ACPDescriptor
	BinaryPath string
	Found      bool
	Hint       string
}

type Registry struct {
	acp     map[string]ACPDescriptor
	presets map[string]ProviderPreset
}

func BuiltInACP() []ACPDescriptor {
	return []ACPDescriptor{
		{
			ID:          "agy",
			Name:        "Google Antigravity",
			Detect:      "agy",
			Command:     []string{"agy", "acp"},
			ProfileArgs: []string{"--app-data-dir", "{profile}", "--gemini-dir", "{profile}/.gemini"},
			Login:       []string{"agy", "acp", "--login", "--app-data-dir", "{profile}", "--gemini-dir", "{profile}/.gemini"},
			DefaultModels: []string{
				"default",
				"gemini-3.1-pro",
			},
			TermsNote: "Runs the official agy CLI over ACP. Check Google Antigravity terms before automated/multi-account use.",
			BuiltIn:   true,
		},
		{
			ID:      "gemini",
			Name:    "Gemini CLI",
			Detect:  "gemini",
			Command: []string{"gemini", "--experimental-acp"},
			ProfileEnv: map[string]string{
				"GEMINI_CLI_HOME": "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			},
			Login: []string{"gemini"},
			DefaultModels: []string{
				"default",
				"gemini-2.5-pro",
				"gemini-2.5-flash",
			},
			TermsNote: "Runs the official Gemini CLI in ACP mode. Review Google terms for programmatic usage.",
			BuiltIn:   true,
		},
		{
			ID:      "codex",
			Name:    "OpenAI Codex (ACP)",
			Detect:  "codex-acp",
			Command: []string{"codex-acp"},
			ProfileEnv: map[string]string{
				"CODEX_HOME": "{profile}",
			},
			Login: []string{"codex", "login"},
			DefaultModels: []string{
				"default",
			},
			TermsNote: "Requires the official codex-acp adapter and codex login. Review OpenAI subscription terms before third-party client use.",
			BuiltIn:   true,
		},
		{
			ID:      "claude",
			Name:    "Claude Code (ACP)",
			Detect:  "claude-agent-acp",
			Command: []string{"claude-agent-acp"},
			ProfileEnv: map[string]string{
				"CLAUDE_CONFIG_DIR": "{profile}",
			},
			Login: []string{"claude"},
			DefaultModels: []string{
				"default",
			},
			TermsNote: "Requires the claude-agent-acp adapter and Claude Code sign-in. Check Anthropic subscription terms for third-party client use.",
			BuiltIn:   true,
		},
		{
			ID:            "custom-acp",
			Name:          "Custom ACP Command",
			Detect:        "",
			Command:       nil,
			DefaultModels: []string{"default"},
			TermsNote:     "Launches your custom ACP-compatible stdio command with isolated profile directory.",
			BuiltIn:       true,
		},
	}
}

func LoadRegistry(dataDir string) (*Registry, []error) {
	r := &Registry{
		acp:     make(map[string]ACPDescriptor),
		presets: make(map[string]ProviderPreset),
	}
	for _, d := range BuiltInACP() {
		r.acp[d.ID] = d
	}
	for _, p := range BuiltInPresets() {
		r.presets[p.ID] = p
	}

	var errs []error
	dir := filepath.Join(dataDir, "backends.d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return r, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		var d ACPDescriptor
		if err := yaml.Unmarshal(data, &d); err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", path, err))
			continue
		}
		if d.ID == "" {
			d.ID = strings.TrimSuffix(e.Name(), ext)
		}
		if d.Name == "" {
			d.Name = d.ID
		}
		if len(d.DefaultModels) == 0 {
			d.DefaultModels = []string{"default"}
		}
		d.BuiltIn = false
		d.SourcePath = path
		r.acp[d.ID] = d
	}
	return r, errs
}

func (r *Registry) GetACP(id string) (ACPDescriptor, bool) {
	d, ok := r.acp[id]
	return d, ok
}

func (r *Registry) ListACP() []ACPDescriptor {
	order := map[string]int{"agy": 1, "gemini": 2, "codex": 3, "claude": 4, "custom-acp": 99}
	out := make([]ACPDescriptor, 0, len(r.acp))
	for _, d := range r.acp {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, okI := order[out[i].ID]
		oj, okJ := order[out[j].ID]
		if okI && okJ && oi != oj {
			return oi < oj
		}
		if okI != okJ {
			return okI
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (r *Registry) GetPreset(id string) (ProviderPreset, bool) {
	p, ok := r.presets[id]
	return p, ok
}

func (r *Registry) ListPresets() []ProviderPreset {
	order := map[string]int{
		"deepseek":   1,
		"openrouter": 2,
		"openai":     3,
		"anthropic":  4,
		"google":     5,
		"groq":       6,
		"together":   7,
		"mistral":    8,
		"xai":        9,
		"ollama":     10,
		"custom":     99,
	}
	out := make([]ProviderPreset, 0, len(r.presets))
	for _, p := range r.presets {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, okI := order[out[i].ID]
		oj, okJ := order[out[j].ID]
		if okI && okJ && oi != oj {
			return oi < oj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// DetectInstalled scans PATH for every known ACP descriptor binary.
func (r *Registry) DetectInstalled() []DetectedCLI {
	descs := r.ListACP()
	out := make([]DetectedCLI, 0, len(descs))
	for _, d := range descs {
		if d.Detect == "" {
			continue
		}
		item := DetectedCLI{Descriptor: d}
		if p, err := exec.LookPath(d.Detect); err == nil {
			item.Found = true
			item.BinaryPath = p
		} else {
			// Give helpful adapter hints for codex and claude if base CLI is installed.
			switch d.ID {
			case "codex":
				if _, err2 := exec.LookPath("codex"); err2 == nil {
					item.Hint = "codex found; install codex-acp (@agentclientprotocol/codex-acp) for ACP mode"
				}
			case "claude":
				if _, err2 := exec.LookPath("claude"); err2 == nil {
					item.Hint = "claude found; install claude-agent-acp (@agentclientprotocol/claude-agent-acp) for ACP mode"
				}
			}
		}
		out = append(out, item)
	}
	return out
}

// ExpandCommand substitutes {profile} in command + profile_args.
func (d ACPDescriptor) ExpandCommand(profileDir string, customCmd []string) []string {
	var parts []string
	if len(customCmd) > 0 {
		parts = append(parts, customCmd...)
	} else {
		parts = append(parts, d.Command...)
		parts = append(parts, d.ProfileArgs...)
	}
	out := make([]string, len(parts))
	for i, s := range parts {
		out[i] = strings.ReplaceAll(s, "{profile}", profileDir)
	}
	return out
}

// ExpandLogin substitutes {profile} in login command.
func (d ACPDescriptor) ExpandLogin(profileDir string, customLogin []string) []string {
	src := d.Login
	if len(customLogin) > 0 {
		src = customLogin
	}
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = strings.ReplaceAll(s, "{profile}", profileDir)
	}
	return out
}

// ExpandEnv returns KEY=VALUE entries to merge into child process env.
func (d ACPDescriptor) ExpandEnv(profileDir string, customEnv map[string]string) []string {
	merged := make(map[string]string)
	for k, v := range d.ProfileEnv {
		merged[k] = strings.ReplaceAll(v, "{profile}", profileDir)
	}
	for k, v := range customEnv {
		merged[k] = strings.ReplaceAll(v, "{profile}", profileDir)
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
