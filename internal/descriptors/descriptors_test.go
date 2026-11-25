package descriptors

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuiltInRegistryAndCustomYAMLDescriptors(t *testing.T) {
	dir := t.TempDir()
	bd := filepath.Join(dir, "backends.d")
	if err := os.MkdirAll(bd, 0o700); err != nil {
		t.Fatal(err)
	}
	customYAML := `id: myagent
name: My Custom Agent
detect: myagent
command: ["myagent", "acp"]
profile_args: ["--home", "{profile}"]
profile_env:
  MYAGENT_DIR: "{profile}/data"
login: ["myagent", "login", "--home", "{profile}"]
default_models: ["m1", "m2"]
`
	if err := os.WriteFile(filepath.Join(bd, "myagent.yaml"), []byte(customYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, errs := LoadRegistry(dir)
	if len(errs) != 0 {
		t.Fatalf("unexpected load errs: %v", errs)
	}

	// Verify agy uses relative flags + isolated HOME={profile} (prevents "must not be absolute" error)
	agy, ok := reg.GetACP("agy")
	if !ok {
		t.Fatalf("missing built-in agy descriptor")
	}
	gotAgy := agy.ExpandCommand("/tmp/prof", nil)
	wantAgy := []string{"agy", "acp", "--app-data-dir", "acpx-prof", "--gemini-dir", ".gemini"}
	if !reflect.DeepEqual(gotAgy, wantAgy) {
		t.Fatalf("agy expand mismatch: got=%v want=%v", gotAgy, wantAgy)
	}
	gotEnv := agy.ExpandEnv("/tmp/prof", nil)
	wantEnv := []string{"HOME=/tmp/prof", "XDG_CONFIG_HOME=/tmp/prof/.config"}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("agy env mismatch: got=%v want=%v", gotEnv, wantEnv)
	}

	// Verify all requested ACP agents and official registry agents are registered
	required := []string{
		"agy", "codex", "claude", "cursor", "grok", "opencode", "muse-code", "pi", "cline",
		"gemini", "agoragentic-acp", "amp-acp", "auggie", "autohand", "bob", "codebuddy-code",
		"cortex-code", "corust-agent", "crow-cli", "deepagents", "devin", "dimcode", "dirac",
		"factory-droid", "fast-agent", "github-copilot-cli", "glm-acp-agent", "goose", "harn",
		"junie", "kilo", "kimchi", "kimi", "kimi-code", "kiro", "minimax-code", "minion-code",
		"mistral-vibe", "nova", "poolside", "qoder", "qwen-code", "sigit", "stakpak", "vtcode",
	}
	for _, id := range required {
		if _, ok := reg.GetACP(id); !ok {
			t.Errorf("expected ACP descriptor %q to be registered", id)
		}
	}

	my, ok := reg.GetACP("myagent")
	if !ok || my.BuiltIn {
		t.Fatalf("expected custom myagent descriptor: ok=%v builtIn=%v", ok, my.BuiltIn)
	}
	if !reflect.DeepEqual(my.ExpandCommand("/p", nil), []string{"myagent", "acp", "--home", "/p"}) {
		t.Fatalf("unexpected custom expand: %v", my.ExpandCommand("/p", nil))
	}
	if !reflect.DeepEqual(my.ExpandEnv("/p", nil), []string{"MYAGENT_DIR=/p/data"}) {
		t.Fatalf("unexpected custom env: %v", my.ExpandEnv("/p", nil))
	}

	if _, ok := reg.GetPreset("deepseek"); !ok {
		t.Fatalf("missing deepseek preset")
	}

	// Verify grok has login command and isolated HOME for both ACP and Direct CLI
	grokACP, ok := reg.GetACP("grok")
	if !ok {
		t.Fatalf("missing grok ACP descriptor")
	}
	if !reflect.DeepEqual(grokACP.ExpandLogin("/tmp/grok-prof", nil), []string{"grok", "login"}) {
		t.Fatalf("unexpected grok ACP login: %v", grokACP.ExpandLogin("/tmp/grok-prof", nil))
	}
	if !reflect.DeepEqual(grokACP.ExpandEnv("/tmp/grok-prof", nil), []string{"HOME=/tmp/grok-prof", "XDG_CONFIG_HOME=/tmp/grok-prof/.config"}) {
		t.Fatalf("unexpected grok ACP env: %v", grokACP.ExpandEnv("/tmp/grok-prof", nil))
	}
	grokCLI, ok := reg.GetCLI("grok")
	if !ok || !reflect.DeepEqual(grokCLI.LoginCmd, []string{"grok", "login"}) {
		t.Fatalf("unexpected grok CLI descriptor: ok=%v login=%v", ok, grokCLI.LoginCmd)
	}
}
