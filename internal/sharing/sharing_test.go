package sharing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/skills"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func states(items []Item) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.Name+" "+i.Detail] = i.State
	}
	return out
}

func TestExistingServersStayOnAndNewOrChangedOnesWait(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, `{"mcpServers":{"browse":{"command":"node","args":["a"],"env":{"K":"1"}}},
		"projects":{"/work":{"mcpServers":{"db":{"url":"https://db.example/mcp?token=s3cret"}}}}}`)
	accounts := []config.AccountConfig{{Kind: config.KindACP, Backend: "claude"}}
	c, err := Open(data, home, accounts)
	if err != nil {
		t.Fatal(err)
	}
	list := func() []Item {
		t.Helper()
		c, err := Open(data, home, accounts)
		if err != nil {
			t.Fatal(err)
		}
		items, err := List(c, home, "", Agents(accounts))
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	got := states(list())
	if got["browse node"] != On || got["db db.example"] != On {
		t.Fatalf("existing servers: %v", got)
	}

	// A rotated secret keeps the approval; a new command or server loses it.
	write(t, claude, `{"mcpServers":{"browse":{"command":"node","args":["a"],"env":{"K":"2"}}}}`)
	if got := states(list()); got["browse node"] != On {
		t.Fatalf("after a secret changed: %v", got)
	}
	write(t, claude, `{"mcpServers":{"browse":{"command":"node","args":["b"]},"extra":{"command":"sh"}}}`)
	items := list()
	if got := states(items); got["browse node"] != New || got["extra sh"] != New || Pending(items) != 2 {
		t.Fatalf("after changes: %v", got)
	}
	c, _ = Load(data)
	if n := Unannounced(data, c, items); n != 2 {
		t.Fatalf("first announcement = %d", n)
	}
	c, _ = Load(data)
	if n := Unannounced(data, c, items); n != 0 {
		t.Fatalf("announced twice: %d", n)
	}

	c, _ = Load(data)
	c.Set(items[1], false)
	if err := Save(data, c); err != nil {
		t.Fatal(err)
	}
	if got := states(list()); got["extra sh"] != Off {
		t.Fatalf("after refusing: %v", got)
	}
}

func TestNothingIsApprovedWithoutAccounts(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"browse":{"command":"node"}}}`)
	if _, err := Open(data, home, nil); err != nil {
		t.Fatal(err)
	}
	accounts := []config.AccountConfig{{Kind: config.KindACP, Backend: "claude"}}
	c, err := Open(data, home, accounts)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := List(c, home, "", Agents(accounts))
	if Pending(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
}

func TestSkillsOffKeepsProjectSkills(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	write(t, filepath.Join(home, ".agents/skills/mine/SKILL.md"), "")
	write(t, filepath.Join(project, ".agents/skills/ours/SKILL.md"), "")
	var c Choices
	c.Set(Item{Kind: KindSkill, Name: "mine"}, false)
	c.Set(Item{Kind: KindSkill, Name: "ours"}, false)
	got := c.Skills(skills.Discover(home, project), project)
	if len(got) != 1 || got[0].Name != "ours" {
		t.Fatalf("skills: %+v", got)
	}
}
