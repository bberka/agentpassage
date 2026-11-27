package acpbridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/secrets"
)

// TestMain gives the package an empty home, so the developer's own skills do
// not add messages to the bridge output.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "apx-bridge-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// T3 Code lists skills from available_commands_update names that start with "$".
func TestSessionNewListsSkills(t *testing.T) {
	project := t.TempDir()
	skill := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: Demo skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, config.Default(), secrets.NewFileOnly(dir), reg, nil)

	runBridgeInteractive(t, gw, func(send func(string), readLine func() map[string]any) {
		send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"` + filepath.ToSlash(project) + `"}}`)
		if reply := readLine(); reply["result"] == nil {
			t.Fatalf("session/new: %v", reply)
		}
		params, _ := readLine()["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		commands, _ := update["availableCommands"].([]any)
		if update["sessionUpdate"] != "available_commands_update" || len(commands) != 1 {
			t.Fatalf("got %v", params)
		}
		if got := commands[0].(map[string]any); got["name"] != "$demo" || got["description"] != "Demo skill" {
			t.Fatalf("got %v", got)
		}
	})
}
