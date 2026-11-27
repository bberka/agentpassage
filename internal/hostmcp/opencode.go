package hostmcp

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
)

type openCodeServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Enabled     *bool             `json:"enabled"`
	Disabled    bool              `json:"disabled"`
}

var openCodeVar = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// loadOpenCode reads both layouts OpenCode and its fork Kilo accept: servers
// directly under "mcp", and under "mcp.servers". It reads <base>.json and
// <base>.jsonc for each base, and a later file wins for a repeated name.
func loadOpenCode(dir string, bases ...string) (map[string]server, error) {
	var names []string
	for _, base := range bases {
		names = append(names, base+".json", base+".jsonc")
	}
	out := make(map[string]server)
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		invalid := fmt.Errorf("can't read MCP servers: %s is not valid", path)
		var file struct {
			MCP map[string]json.RawMessage `json:"mcp"`
		}
		if json.Unmarshal(plainJSON(data), &file) != nil {
			return nil, invalid
		}
		entries := maps.Clone(file.MCP)
		if nested, ok := file.MCP["servers"]; ok {
			var servers map[string]json.RawMessage
			if json.Unmarshal(nested, &servers) != nil {
				return nil, invalid
			}
			delete(entries, "servers")
			if entries == nil {
				entries = map[string]json.RawMessage{}
			}
			maps.Copy(entries, servers)
		}
		for id, raw := range entries {
			var s openCodeServer
			// Other "mcp" settings, such as a timeout, are not servers.
			if json.Unmarshal(raw, &s) != nil || (len(s.Command) == 0 && s.URL == "") {
				continue
			}
			if s.Disabled || (s.Enabled != nil && !*s.Enabled) {
				delete(out, id)
				continue
			}
			conv := server{URL: s.URL, Env: expand(openCodeVar, s.Environment), Headers: expand(openCodeVar, s.Headers)}
			if len(s.Command) > 0 {
				conv.Command, conv.Args = s.Command[0], s.Command[1:]
			}
			out[id] = conv
		}
	}
	return out, nil
}

// expand replaces each reference to an environment variable, which ref matches
// with the name as its first group.
func expand(ref *regexp.Regexp, m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = ref.ReplaceAllStringFunc(v, func(found string) string {
			return os.Getenv(ref.FindStringSubmatch(found)[1])
		})
	}
	return out
}

// jsonNoise matches a string, a comment, or a comma before a closing bracket.
var jsonNoise = regexp.MustCompile(`("(?:\\.|[^"\\])*")|//[^\n]*|(?s:/\*.*?\*/)|,(\s*[}\]])`)

// plainJSON turns JSON with comments and trailing commas into plain JSON.
func plainJSON(data []byte) []byte {
	// Comments go first, so one between a comma and a bracket can't hide it.
	for range 2 {
		data = jsonNoise.ReplaceAll(data, []byte("$1$2"))
	}
	return data
}
