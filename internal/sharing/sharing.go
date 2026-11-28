// Package sharing keeps the user's choice of which MCP servers and skills apx
// may give to accounts. One choice covers every account.
package sharing

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/hostmcp"
	"github.com/bberka/agentpassage/internal/skills"
)

// Choices is the content of tools.json in the data directory. It holds names
// and fingerprints only.
type Choices struct {
	// MCP maps a server name to the answer for each fingerprint seen under it.
	// A server with no answer is new and stays off.
	MCP       map[string]map[string]bool `json:"mcp"`
	SkillsOff []string                   `json:"skills_off,omitempty"`
	Notified  []string                   `json:"notified,omitempty"`
}

// Item states.
const (
	On  = "on"
	Off = "off"
	New = "new"
)

// Item kinds.
const (
	KindMCP   = "mcp"
	KindSkill = "skill"
)

// Item is one MCP server or skill and what apx does with it.
type Item struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	State       string   `json:"state"`
	Agents      []string `json:"agents,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Project     bool     `json:"project,omitempty"` // a project skill, which can't be switched off
	Fingerprint string   `json:"-"`
}

func path(dataDir string) string { return filepath.Join(dataDir, "tools.json") }

// Agents lists the agents whose accounts run in an isolated profile, which are
// the ones apx shares tools with.
func Agents(accounts []config.AccountConfig) []string {
	var out []string
	for _, a := range accounts {
		if (a.Kind == config.KindACP || a.Kind == config.KindCLI) && !a.UseHostProfile && !slices.Contains(out, a.Backend) {
			out = append(out, a.Backend)
		}
	}
	slices.Sort(out)
	return out
}

// Open reads the choices. The first time, it approves the servers that the
// existing accounts already get, so an upgrade changes nothing; without
// accounts nothing is approved.
func Open(dataDir, home string, accounts []config.AccountConfig) (Choices, error) {
	c, err := Load(dataDir)
	if !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	c = Choices{MCP: map[string]map[string]bool{}}
	for _, agent := range Agents(accounts) {
		// An unreadable config has no servers to keep.
		servers, _ := hostmcp.LoadAll(home, agent, "")
		for _, s := range servers {
			c.set(s.Name, s.Fingerprint, true)
		}
	}
	return c, Save(dataDir, c)
}

// Load reads the choices.
func Load(dataDir string) (Choices, error) {
	c := Choices{MCP: map[string]map[string]bool{}}
	data, err := os.ReadFile(path(dataDir))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Choices{}, fmt.Errorf("%s is not valid", path(dataDir))
	}
	if c.MCP == nil {
		c.MCP = map[string]map[string]bool{}
	}
	return c, nil
}

// Save writes the choices, replacing the file in one step.
func Save(dataDir string, c Choices) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, "tools-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dataDir))
}

func (c *Choices) set(name, fingerprint string, on bool) {
	if c.MCP[name] == nil {
		c.MCP[name] = map[string]bool{}
	}
	c.MCP[name][fingerprint] = on
}

// Allows reports whether the user approved this server.
func (c Choices) Allows(name, fingerprint string) bool { return c.MCP[name][fingerprint] }

// Set records the answer for an item.
func (c *Choices) Set(item Item, on bool) {
	if item.Kind == KindMCP {
		c.set(item.Name, item.Fingerprint, on)
		return
	}
	c.SkillsOff = slices.DeleteFunc(c.SkillsOff, func(n string) bool { return n == item.Name })
	if !on {
		c.SkillsOff = append(c.SkillsOff, item.Name)
		slices.Sort(c.SkillsOff)
	}
}

// Skills drops the skills the user switched off.
func (c Choices) Skills(found []skills.Skill, cwd string) []skills.Skill {
	return slices.DeleteFunc(slices.Clone(found), func(s skills.Skill) bool {
		return slices.Contains(c.SkillsOff, s.Name) && !inProject(s.Path, cwd)
	})
}

func inProject(file, cwd string) bool {
	if home, _ := os.UserHomeDir(); cwd == "" || cwd == home {
		return false
	}
	return strings.HasPrefix(file, filepath.Clean(cwd)+string(filepath.Separator))
}

// List returns the MCP servers of the given agents, then the skills, with
// their states. cwd adds a project's skills.
func List(c Choices, home, cwd string, agents []string) ([]Item, error) {
	var out []Item
	var failed []error
	index := make(map[string]int)
	for _, agent := range agents {
		servers, err := hostmcp.LoadAll(home, agent, cwd)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		for _, s := range servers {
			key := s.Name + "\x00" + s.Fingerprint
			if i, ok := index[key]; ok {
				out[i].Agents = append(out[i].Agents, agent)
				continue
			}
			state := New
			if on, answered := c.MCP[s.Name][s.Fingerprint]; answered {
				state = Off
				if on {
					state = On
				}
			}
			index[key] = len(out)
			out = append(out, Item{Kind: KindMCP, Name: s.Name, State: state, Agents: []string{agent}, Detail: s.Summary, Fingerprint: s.Fingerprint})
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int { return cmp.Compare(a.Name, b.Name) })
	for _, s := range skills.Discover(home, cwd) {
		item := Item{Kind: KindSkill, Name: s.Name, State: On, Project: inProject(s.Path, cwd)}
		if !item.Project && slices.Contains(c.SkillsOff, s.Name) {
			item.State = Off
		}
		out = append(out, item)
	}
	return out, errors.Join(failed...)
}

// Pending counts the servers waiting for an answer.
func Pending(items []Item) int {
	n := 0
	for _, i := range items {
		if i.State == New {
			n++
		}
	}
	return n
}

// PendingText is the one line apx shows when servers wait for an answer.
func PendingText(n int) string {
	if n == 1 {
		return "1 new MCP server to review"
	}
	return fmt.Sprintf("%d new MCP servers to review", n)
}

// Unannounced returns how many servers wait for an answer when at least one of
// them wasn't announced before, and remembers them. Otherwise it returns 0.
func Unannounced(dataDir string, c Choices, items []Item) int {
	fresh := false
	for _, i := range items {
		key := i.Name + "/" + i.Fingerprint
		if i.State == New && !slices.Contains(c.Notified, key) {
			c.Notified = append(c.Notified, key)
			fresh = true
		}
	}
	if !fresh || Save(dataDir, c) != nil {
		return 0
	}
	return Pending(items)
}
