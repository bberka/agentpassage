package descriptors

import (
	"os/exec"
	"sort"
	"strings"
)

// CLIDescriptor defines how to proxy requests directly through an official CLI's
// non-interactive / streaming print mode (no ACP adapter required).
type CLIDescriptor struct {
	ID              string            `yaml:"id" json:"id"`
	Name            string            `yaml:"name" json:"name"`
	Description     string            `yaml:"description,omitempty" json:"description,omitempty"`
	Binary          string            `yaml:"binary" json:"binary"`
	BaseArgs        []string          `yaml:"base_args,omitempty" json:"base_args,omitempty"`
	ModelFlag       string            `yaml:"model_flag,omitempty" json:"model_flag,omitempty"`
	ResumeFlag      string            `yaml:"resume_flag,omitempty" json:"resume_flag,omitempty"`
	ResumeSubcmd    bool              `yaml:"resume_subcmd,omitempty" json:"resume_subcmd,omitempty"` // true for `codex exec resume <sid>`
	CwdFlag         string            `yaml:"cwd_flag,omitempty" json:"cwd_flag,omitempty"`
	AutoApproveArgs []string          `yaml:"auto_approve_args,omitempty" json:"auto_approve_args,omitempty"`
	PromptFlag      string            `yaml:"prompt_flag,omitempty" json:"prompt_flag,omitempty"` // empty = positional arg at end
	OutputFormat    string            `yaml:"output_format" json:"output_format"`                // "stream-json", "codex-jsonl", "opencode-json", "text"
	ModelsCmd       []string          `yaml:"models_cmd,omitempty" json:"models_cmd,omitempty"`
	LoginCmd        []string          `yaml:"login_cmd,omitempty" json:"login_cmd,omitempty"`
	ProfileEnv      map[string]string `yaml:"profile_env,omitempty" json:"profile_env,omitempty"`
	DefaultModels   []string          `yaml:"default_models,omitempty" json:"default_models,omitempty"`
}

func BuiltInCLI() []CLIDescriptor {
	return []CLIDescriptor{
		{
			ID:              "agy",
			Name:            "Antigravity CLI (Direct)",
			Description:     "Direct `agy --print --output-format stream-json` proxy",
			Binary:          "agy",
			BaseArgs:        []string{"--print", "--output-format", "stream-json"},
			ModelFlag:       "--model",
			ResumeFlag:      "--conversation",
			AutoApproveArgs: []string{"--dangerously-skip-permissions"},
			OutputFormat:    "stream-json",
			ModelsCmd:       []string{"agy", "models"},
			LoginCmd:        []string{"agy", "acp", "--login", "--app-data-dir", "acpx-{account}", "--gemini-dir", ".gemini"},
			ProfileEnv: map[string]string{
				"HOME":            "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:              "claude",
			Name:            "Claude Code CLI (Direct)",
			Description:     "Direct `claude --print --output-format stream-json` proxy (no adapter needed)",
			Binary:          "claude",
			BaseArgs:        []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages"},
			ModelFlag:       "--model",
			ResumeFlag:      "--resume",
			AutoApproveArgs: []string{"--dangerously-skip-permissions"},
			OutputFormat:    "stream-json",
			LoginCmd:        []string{"claude"},
			ProfileEnv: map[string]string{
				"CLAUDE_CONFIG_DIR": "{profile}",
			},
			DefaultModels: []string{"default", "sonnet", "opus", "haiku"},
		},
		{
			ID:              "codex",
			Name:            "Codex CLI (Direct)",
			Description:     "Direct `codex exec --json` proxy (no codex-acp adapter needed)",
			Binary:          "codex",
			BaseArgs:        []string{"exec", "--json", "--skip-git-repo-check"},
			ModelFlag:       "--model",
			ResumeSubcmd:    true,
			CwdFlag:         "--cd",
			AutoApproveArgs: []string{"--full-auto"},
			OutputFormat:    "codex-jsonl",
			LoginCmd:        []string{"codex", "login"},
			ProfileEnv: map[string]string{
				"CODEX_HOME": "{profile}",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:              "opencode",
			Name:            "OpenCode CLI (Direct)",
			Description:     "Direct `opencode run --format json` proxy",
			Binary:          "opencode",
			BaseArgs:        []string{"run", "--format", "json"},
			ModelFlag:       "--model",
			ResumeFlag:      "--session",
			AutoApproveArgs: []string{"--auto"},
			OutputFormat:    "opencode-json",
			ModelsCmd:       []string{"opencode", "models"},
			LoginCmd:        []string{"opencode", "auth", "login"},
			ProfileEnv: map[string]string{
				"XDG_CONFIG_HOME": "{profile}/.config",
				"XDG_DATA_HOME":   "{profile}/.local/share",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:           "gemini",
			Name:         "Gemini CLI (Direct)",
			Description:  "Direct `gemini --output-format stream-json -p` proxy",
			Binary:       "gemini",
			BaseArgs:     []string{"--output-format", "stream-json"},
			ModelFlag:    "--model",
			PromptFlag:   "-p",
			OutputFormat: "stream-json",
			LoginCmd:     []string{"gemini"},
			ProfileEnv: map[string]string{
				"GEMINI_CLI_HOME": "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:           "grok",
			Name:         "Grok CLI (Direct)",
			Description:  "Direct `grok -p` proxy",
			Binary:       "grok",
			PromptFlag:   "-p",
			OutputFormat: "text",
			ProfileEnv: map[string]string{
				"HOME": "{profile}",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:           "cursor",
			Name:         "Cursor Agent CLI (Direct)",
			Description:  "Direct `agent --print --output-format stream-json` proxy",
			Binary:       "agent",
			BaseArgs:     []string{"--print", "--output-format", "stream-json"},
			ModelFlag:    "--model",
			OutputFormat: "stream-json",
			LoginCmd:     []string{"agent", "login"},
			ProfileEnv: map[string]string{
				"HOME": "{profile}",
			},
			DefaultModels: []string{"default"},
		},
		{
			ID:            "custom-cli",
			Name:          "Custom CLI Command",
			Description:   "Any CLI command with {prompt}, {model}, {session} placeholders",
			OutputFormat:  "text",
			DefaultModels: []string{"default"},
		},
	}
}

func (r *Registry) GetCLI(id string) (CLIDescriptor, bool) {
	for _, d := range BuiltInCLI() {
		if d.ID == id {
			return d, true
		}
	}
	return CLIDescriptor{}, false
}

func (r *Registry) ListCLI() []CLIDescriptor {
	list := BuiltInCLI()
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ID == "custom-cli" {
			return false
		}
		if list[j].ID == "custom-cli" {
			return true
		}
		return i < j
	})
	return list
}

func (d CLIDescriptor) FoundOnPath() (string, bool) {
	if d.Binary == "" {
		return "", false
	}
	p, err := exec.LookPath(d.Binary)
	return p, err == nil
}

func (d CLIDescriptor) ExpandEnv(profileDir, accountName string, customEnv map[string]string) []string {
	merged := make(map[string]string)
	for k, v := range d.ProfileEnv {
		v = strings.ReplaceAll(v, "{profile}", profileDir)
		v = strings.ReplaceAll(v, "{account}", accountName)
		merged[k] = v
	}
	for k, v := range customEnv {
		v = strings.ReplaceAll(v, "{profile}", profileDir)
		v = strings.ReplaceAll(v, "{account}", accountName)
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
