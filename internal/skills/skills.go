// Package skills finds the user's agent skills (folders holding a SKILL.md),
// so apx can list them to its host and share them with isolated agent profiles.
package skills

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Path        string // absolute path of SKILL.md
}

// Discover lists project skills under cwd and then user skills under home. The
// first skill with a name wins, so a project can override a user skill.
func Discover(home, cwd string) []Skill {
	var roots []string
	for _, base := range []string{cwd, home} {
		if filepath.IsAbs(base) {
			roots = append(roots, filepath.Join(base, ".agents", "skills"), filepath.Join(base, ".claude", "skills"))
		}
	}
	seen := make(map[string]bool)
	var out []Skill
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			sk, ok := read(filepath.Join(root, e.Name(), "SKILL.md"), e.Name())
			if ok && !seen[sk.Name] {
				seen[sk.Name] = true
				out = append(out, sk)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// read parses the name and description from a SKILL.md frontmatter block.
func read(path, dirName string) (Skill, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, false
	}
	defer f.Close()
	sk := Skill{Name: dirName, Path: path}
	sc := bufio.NewScanner(f)
	if sc.Scan() && strings.TrimSpace(sc.Text()) == "---" {
		inDescription := false
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) == "---" {
				break
			}
			// Indented lines continue a block or folded description.
			if inDescription && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
				sk.Description = strings.TrimSpace(sk.Description + " " + strings.TrimSpace(line))
				continue
			}
			inDescription = false
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = unquote(strings.TrimSpace(value))
			switch key {
			case "name":
				if value != "" {
					sk.Name = value
				}
			case "description":
				inDescription = true
				if value != "" && strings.Trim(value, ">|+-") != "" {
					sk.Description = value
				}
			}
		}
	}
	return sk, validName.MatchString(sk.Name)
}

func unquote(s string) string {
	if v, err := strconv.Unquote(s); err == nil {
		return v
	}
	return strings.Trim(s, `'"`)
}

var mention = regexp.MustCompile(`(?:^|\s)\$([A-Za-z0-9][A-Za-z0-9._:-]*)`)

// Instructions tells an agent where to find each skill the text mentions as
// "$name", so a skill works even in an agent that does not load it itself. It
// returns "" when the text names none of them.
func Instructions(text string, available []Skill) string {
	byName := make(map[string]Skill, len(available))
	for _, s := range available {
		byName[s.Name] = s
	}
	var lines []string
	seen := make(map[string]bool)
	for _, m := range mention.FindAllStringSubmatch(text, -1) {
		name := strings.TrimRight(m[1], ".:")
		if s, ok := byName[name]; ok && !seen[name] {
			seen[name] = true
			lines = append(lines, "$"+name+" is a skill: read "+s.Path+" and follow it.")
		}
	}
	return strings.Join(lines, "\n")
}

// Share links each skill in home/.agents/skills into dir. It only adds missing
// entries and never replaces or removes anything already there.
func Share(home, dir string) error {
	source := filepath.Join(home, ".agents", "skills")
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		target := filepath.Join(source, e.Name())
		if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
			continue
		}
		link := filepath.Join(dir, e.Name())
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(target, link); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}
