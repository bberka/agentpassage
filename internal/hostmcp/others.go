package hostmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// loadGoose reads the command and remote extensions from Goose's config. Its
// other extensions are built into Goose.
func loadGoose(path string) (map[string]server, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Extensions map[string]struct {
			Type    string            `yaml:"type"`
			Cmd     string            `yaml:"cmd"`
			Args    []string          `yaml:"args"`
			Envs    map[string]string `yaml:"envs"`
			URI     string            `yaml:"uri"`
			Headers map[string]string `yaml:"headers"`
			Enabled *bool             `yaml:"enabled"`
		} `yaml:"extensions"`
	}
	if yaml.Unmarshal(data, &file) != nil {
		return nil, fmt.Errorf("can't read MCP servers: %s is not valid", path)
	}
	out := make(map[string]server)
	for name, e := range file.Extensions {
		if e.Enabled != nil && !*e.Enabled {
			continue
		}
		switch e.Type {
		case "stdio":
			out[name] = server{Command: e.Cmd, Args: e.Args, Env: e.Envs}
		case "streamable_http":
			out[name] = server{URL: e.URI, Headers: e.Headers}
		}
	}
	return out, nil
}

var ampVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// loadAmp reads the "amp.mcpServers" setting from Amp's settings file.
func loadAmp(dir string) (map[string]server, error) {
	out := make(map[string]server)
	for _, name := range []string{"settings.json", "settings.jsonc"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var file struct {
			Servers map[string]server `json:"amp.mcpServers"`
		}
		if json.Unmarshal(plainJSON(data), &file) != nil {
			return nil, fmt.Errorf("can't read MCP servers: %s is not valid", path)
		}
		for id, s := range file.Servers {
			s.Env, s.Headers = expand(ampVar, s.Env), expand(ampVar, s.Headers)
			out[id] = s
		}
	}
	return out, nil
}
