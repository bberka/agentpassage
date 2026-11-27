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
		if err := Share(home, dir); err != nil {
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
