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

// ACPDescriptor defines how to detect, launch, isolate, install, and authenticate an ACP CLI.
type ACPDescriptor struct {
	ID             string            `yaml:"id" json:"id"`
	RegistryID     string            `yaml:"registry_id,omitempty" json:"registry_id,omitempty"`
	Name           string            `yaml:"name" json:"name"`
	Description    string            `yaml:"description,omitempty" json:"description,omitempty"`
	Version        string            `yaml:"version,omitempty" json:"version,omitempty"`
	Website        string            `yaml:"website,omitempty" json:"website,omitempty"`
	Repository     string            `yaml:"repository,omitempty" json:"repository,omitempty"`
	Detect         string            `yaml:"detect" json:"detect"`
	Command        []string          `yaml:"command" json:"command"`
	ProfileArgs    []string          `yaml:"profile_args,omitempty" json:"profile_args,omitempty"`
	ProfileEnv     map[string]string `yaml:"profile_env,omitempty" json:"profile_env,omitempty"`
	Login          []string          `yaml:"login,omitempty" json:"login,omitempty"`
	InstallPackage string            `yaml:"install_package,omitempty" json:"install_package,omitempty"`
	InstallCmd     []string          `yaml:"install_cmd,omitempty" json:"install_cmd,omitempty"`
	InstallNote    string            `yaml:"install_note,omitempty" json:"install_note,omitempty"`
	NPXPackage     string            `yaml:"npx_package,omitempty" json:"npx_package,omitempty"`
	NPXArgs        []string          `yaml:"npx_args,omitempty" json:"npx_args,omitempty"`
	UVXPackage     string            `yaml:"uvx_package,omitempty" json:"uvx_package,omitempty"`
	UVXArgs        []string          `yaml:"uvx_args,omitempty" json:"uvx_args,omitempty"`
	BinaryArchive  string            `yaml:"binary_archive,omitempty" json:"binary_archive,omitempty"`
	BinarySHA256   string            `yaml:"binary_sha256,omitempty" json:"binary_sha256,omitempty"`
	BinaryCmd      string            `yaml:"binary_cmd,omitempty" json:"binary_cmd,omitempty"`
	BinaryArgs     []string          `yaml:"binary_args,omitempty" json:"binary_args,omitempty"`
	DefaultModels  []string          `yaml:"default_models,omitempty" json:"default_models,omitempty"`
	TermsNote      string            `yaml:"terms_note,omitempty" json:"terms_note,omitempty"`
	BuiltIn        bool              `yaml:"-" json:"built_in"`
	FromRegistry   bool              `yaml:"-" json:"from_registry,omitempty"`
	SourcePath     string            `yaml:"-" json:"source_path,omitempty"`
}

type DetectedCLI struct {
	Descriptor ACPDescriptor
	BinaryPath string
	Found      bool
	Hint       string
}

type Registry struct {
	dataDir string
	acp     map[string]ACPDescriptor
	presets map[string]ProviderPreset
}

func BuiltInACP() []ACPDescriptor {
	return []ACPDescriptor{
		{
			ID:          "agy",
			Name:        "Google Antigravity",
			Description: "Google's AI coding agent (official agy CLI)",
			Detect:      "agy",
			Command:     []string{"agy", "acp"},
			// NOTE: agy acp requires RELATIVE names for --app-data-dir and --gemini-dir;
			// setting HOME={profile} isolates each account's credentials completely inside {profile}.
			ProfileArgs: []string{"--app-data-dir", "antigravity-cli", "--gemini-dir", ".gemini"},
			ProfileEnv: map[string]string{
				"HOME":            "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			},
			Login:       []string{"agy", "acp", "--login", "--app-data-dir", "antigravity-cli", "--gemini-dir", ".gemini"},
			InstallNote: "Install Google Antigravity CLI (agy) from your Antigravity installer.",
			DefaultModels: []string{
				"default",
				"gemini-3.1-pro",
			},
			TermsNote: "Runs the official agy CLI over ACP with isolated HOME per account.",
			BuiltIn:   true,
		},
		{
			ID:            "custom-acp",
			Name:          "Custom ACP Command",
			Description:   "Any ACP-compatible stdio JSON-RPC command",
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
		dataDir: dataDir,
		acp:     make(map[string]ACPDescriptor),
		presets: make(map[string]ProviderPreset),
	}

	// 1. Load official ACP Registry (cached synced copy if present, else embedded 46-agent snapshot).
	regBytes := embeddedRegistryJSON
	if dataDir != "" {
		if cached, err := os.ReadFile(filepath.Join(dataDir, "registry.json")); err == nil && len(cached) > 0 {
			regBytes = cached
		}
	}
	if regDescs, err := parseRegistryDescriptors(regBytes, dataDir); err == nil {
		for _, d := range regDescs {
			r.acp[d.ID] = d
		}
	}

	// 2. Layer core built-ins (agy native CLI, custom-acp).
	for _, d := range BuiltInACP() {
		r.acp[d.ID] = d
	}
	for _, p := range BuiltInPresets() {
		r.presets[p.ID] = p
	}

	// 3. Layer user descriptors from backends.d/*.yaml.
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
	if d, ok := r.acp[id]; ok {
		return d, true
	}
	// Also match by official registry ID alias (e.g. "codex-acp" -> "codex")
	if alias, ok := registryIDAlias[id]; ok {
		d, ok2 := r.acp[alias]
		return d, ok2
	}
	return ACPDescriptor{}, false
}

func (r *Registry) ListACP() []ACPDescriptor {
	// Put the primary user-requested tools at the top, followed alphabetically by all other registry agents.
	order := map[string]int{
		"agy":        1,
		"codex":      2,
		"claude":     3,
		"cursor":     4,
		"grok":       5,
		"opencode":   6,
		"muse-code":  7,
		"pi":         8,
		"cline":      9,
		"gemini":     10,
		"custom-acp": 999,
	}
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
			if okI && oi == 999 {
				return false
			}
			if okJ && oj == 999 {
				return true
			}
			return okI
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
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

// FindLocalBinary checks <dataDir>/bin/<id>/ first, then PATH.
func (r *Registry) FindLocalBinary(d ACPDescriptor) (string, bool) {
	if r.dataDir != "" && d.BinaryCmd != "" {
		candidate := filepath.Join(r.dataDir, "bin", d.ID, strings.TrimPrefix(d.BinaryCmd, "./"))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	if d.Detect != "" {
		if p, err := exec.LookPath(d.Detect); err == nil {
			return p, true
		}
	}
	return "", false
}

// DetectInstalled scans local acpx bin dir and PATH for every known ACP descriptor.
func (r *Registry) DetectInstalled() []DetectedCLI {
	descs := r.ListACP()
	out := make([]DetectedCLI, 0, len(descs))
	for _, d := range descs {
		if d.ID == "custom-acp" {
			continue
		}
		item := DetectedCLI{Descriptor: d}
		if p, ok := r.FindLocalBinary(d); ok {
			item.Found = true
			item.BinaryPath = p
		} else {
			switch d.ID {
			case "codex":
				if _, err2 := exec.LookPath("codex"); err2 == nil {
					item.Hint = "codex found; press 'i' to install codex-acp"
				}
			case "claude":
				if _, err2 := exec.LookPath("claude"); err2 == nil {
					item.Hint = "claude found; press 'i' to install claude-agent-acp"
				}
			default:
				if d.NPXPackage != "" {
					item.Hint = "npx " + d.NPXPackage
				} else if d.UVXPackage != "" {
					item.Hint = "uvx " + d.UVXPackage
				} else if d.BinaryArchive != "" {
					item.Hint = "press 'i' to download prebuilt binary"
				}
			}
		}
		out = append(out, item)
	}
	return out
}

// ExpandCommand substitutes {profile} in command + profile_args, preferring locally installed binaries.
func (d ACPDescriptor) ExpandCommand(profileDir string, customCmd []string) []string {
	var parts []string
	if len(customCmd) > 0 {
		parts = append(parts, customCmd...)
	} else {
		// If the descriptor has a direct Detect binary on PATH and Command defaults to npx, prefer the installed binary!
		if len(d.Command) > 0 && d.Command[0] == "npx" && d.Detect != "" {
			if p, err := exec.LookPath(d.Detect); err == nil {
				parts = append(parts, p)
				parts = append(parts, d.NPXArgs...)
			}
		}
		if len(parts) == 0 {
			parts = append(parts, d.Command...)
		}
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

// CanInstall reports whether acpx knows how to install this ACP CLI automatically (binary, npm/npx, or uvx).
func (d ACPDescriptor) CanInstall() bool {
	return len(d.InstallCmd) > 0 || d.InstallPackage != "" || d.BinaryArchive != "" || d.UVXPackage != ""
}

// ResolveInstallCommand returns a shell command for npm/uvx/custom install, or nil when BinaryArchive should be used.
func (d ACPDescriptor) ResolveInstallCommand() ([]string, error) {
	if len(d.InstallCmd) > 0 {
		return append([]string(nil), d.InstallCmd...), nil
	}
	if d.InstallPackage != "" {
		if _, err := exec.LookPath("npm"); err == nil {
			return []string{"npm", "install", "-g", d.InstallPackage}, nil
		}
		if _, err := exec.LookPath("pnpm"); err == nil {
			return []string{"pnpm", "add", "-g", d.InstallPackage}, nil
		}
		if _, err := exec.LookPath("bun"); err == nil {
			return []string{"bun", "add", "-g", d.InstallPackage}, nil
		}
		return nil, fmt.Errorf("npm, pnpm, or bun is required on PATH to install %s", d.InstallPackage)
	}
	if d.UVXPackage != "" {
		if _, err := exec.LookPath("uv"); err == nil {
			return []string{"uv", "tool", "install", d.UVXPackage}, nil
		}
		return nil, fmt.Errorf("uv is required on PATH to install %s", d.UVXPackage)
	}
	if d.BinaryArchive != "" {
		return nil, nil // caller should use InstallBinary
	}
	if d.InstallNote != "" {
		return nil, fmt.Errorf("%s", d.InstallNote)
	}
	return nil, fmt.Errorf("no automatic installer configured for %s", d.Name)
}
