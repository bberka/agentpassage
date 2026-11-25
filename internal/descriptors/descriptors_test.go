package descriptors

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuiltInAndCustomYAMLDescriptors(t *testing.T) {
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

	agy, ok := reg.GetACP("agy")
	if !ok {
		t.Fatalf("missing built-in agy descriptor")
	}
	gotAgy := agy.ExpandCommand("/tmp/prof", nil)
	wantAgy := []string{"agy", "acp", "--app-data-dir", "/tmp/prof", "--gemini-dir", "/tmp/prof/.gemini"}
	if !reflect.DeepEqual(gotAgy, wantAgy) {
		t.Fatalf("agy expand mismatch: got=%v want=%v", gotAgy, wantAgy)
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
}
