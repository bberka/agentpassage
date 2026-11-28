package hostmcp

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Fingerprint identifies what a server runs or connects to, so a changed one
// can be told apart. Env values and headers are left out: they hold secrets
// that rotate.
func (s server) Fingerprint() string {
	data, _ := json.Marshal([]any{s.Type, s.Command, s.Args, cmp.Or(s.URL, s.ServerURL)})
	return fmt.Sprintf("%x", sha256.Sum256(data))[:16]
}

// Summary names the program or host a server uses, without its arguments or
// address path, which can hold secrets.
func (s server) Summary() string {
	if address := cmp.Or(s.URL, s.ServerURL); address != "" {
		if u, err := url.Parse(address); err == nil && u.Host != "" {
			return u.Host
		}
		return "remote"
	}
	return filepath.Base(s.Command)
}

// LoadAll returns every server in agent's config, including the ones Claude
// keeps for single projects. cwd adds nothing more for other agents. A name
// used by different servers appears once per server.
func LoadAll(home, agent, cwd string) ([]Named, error) {
	cwds := []string{cwd}
	if agent == "claude" {
		var file struct {
			Projects map[string]json.RawMessage `json:"projects"`
		}
		if data, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
			_ = json.Unmarshal(data, &file)
		}
		for project := range maps.Keys(file.Projects) {
			cwds = append(cwds, project)
		}
	}
	var out []Named
	seen := make(map[string]bool)
	for _, dir := range cwds {
		servers, err := Load(home, agent, dir)
		if err != nil {
			return nil, err
		}
		for name, s := range servers {
			if s.Command == "" && s.URL == "" && s.ServerURL == "" {
				continue
			}
			n := Named{Name: name, Fingerprint: s.Fingerprint(), Summary: strings.TrimSpace(s.Summary())}
			if !seen[n.Name+"\x00"+n.Fingerprint] {
				seen[n.Name+"\x00"+n.Fingerprint] = true
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// Named describes one configured server without its secrets.
type Named struct {
	Name        string
	Fingerprint string
	Summary     string
}
