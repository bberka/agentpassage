package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDiscoverInstructionsAndShare(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".agents/skills/pr/SKILL.md"), "---\nname: pr\ndescription: \"Open: a PR\"\n---\nbody")
	write(t, filepath.Join(home, ".agents/skills/long/SKILL.md"), "---\ndescription: >\n  Two\n  lines\nname: long\n---\n")
	write(t, filepath.Join(project, ".claude/skills/pr/SKILL.md"), "---\nname: pr\ndescription: project one\n---\n")

	got := Discover(home, project)
	if len(got) != 2 || got[0].Description != "Two lines" || got[1].Description != "project one" {
		t.Fatalf("got %+v", got)
	}
	if got := Discover(home, ""); len(got) != 2 || got[1].Description != "Open: a PR" {
		t.Fatalf("user skills: %+v", got)
	}

	note := Instructions("use $pr, then $pr. Costs $5 and $unknown", got)
	if strings.Count(note, "SKILL.md") != 1 || !strings.Contains(note, "$pr is a skill") {
		t.Fatalf("note %q", note)
	}

	dir := filepath.Join(t.TempDir(), "skills")
	write(t, filepath.Join(dir, "pr/SKILL.md"), "mine")
	for range 2 {
		if err := Share(dir, filepath.Join(home, ".agents", "skills")); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pr/SKILL.md")); string(b) != "mine" {
		t.Fatal("an existing skill was replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "long/SKILL.md")); err != nil {
		t.Fatalf("missing shared skill: %v", err)
	}
}

// A skill added to the agent reaches the profile, and a removed one leaves it.
func TestShareSourcesAndRemovedSkills(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	own, common := filepath.Join(home, ".claude/skills"), filepath.Join(home, ".agents/skills")
	write(t, filepath.Join(own, "mine/SKILL.md"), "own")
	write(t, filepath.Join(own, "both/SKILL.md"), "own")
	write(t, filepath.Join(common, "both/SKILL.md"), "common")
	elsewhere := filepath.Join(dir, "foreign")
	if err := os.Symlink(filepath.Join(home, "missing"), elsewhere); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := Share(dir, own, common); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "both/SKILL.md")); string(b) != "own" {
		t.Fatalf("both = %q, want the first source", b)
	}
	if err := os.RemoveAll(filepath.Join(own, "mine")); err != nil {
		t.Fatal(err)
	}
	if err := Share(dir, own, common); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "mine")); !os.IsNotExist(err) {
		t.Fatalf("link to a removed skill remains: %v", err)
	}
	if _, err := os.Lstat(elsewhere); err != nil {
		t.Fatalf("a link apx did not make was removed: %v", err)
	}
}

func TestOtherAgentsGetTheSkillsTheirProfileHides(t *testing.T) {
	home, profile := t.TempDir(), t.TempDir()
	for _, dir := range []string{".config/goose/skills/own", ".config/agents/skills/common", ".config/muse/skills/meta"} {
		path := filepath.Join(home, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for agent, want := range map[string][]string{
		"goose":     {".config/goose/skills/own", ".config/agents/skills/common"},
		"muse-code": {".config/muse/skills/meta"},
	} {
		if err := ShareWithProfile(agent, home, filepath.Join(profile, agent)); err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if _, err := os.Stat(filepath.Join(profile, agent, w, "SKILL.md")); err != nil {
				t.Errorf("%s profile lacks %s: %v", agent, w, err)
			}
		}
	}
}
