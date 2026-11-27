// Package hostmcp reads the MCP servers from the user's own agent configs, so
// apx can give them to agents whose isolated profile hides those files.
package hostmcp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
)

// Capabilities are the MCP transports an agent accepts besides stdio.
type Capabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// server covers the fields the supported config files use.
type server struct {
	Type           string            `json:"type"`
	Command        string            `json:"command" toml:"command"`
	Args           []string          `json:"args" toml:"args"`
	Env            map[string]string `json:"env" toml:"env"`
	URL            string            `json:"url" toml:"url"`
	ServerURL      string            `json:"serverUrl"`
	Headers        map[string]string `json:"headers" toml:"http_headers"`
	BearerTokenEnv string            `toml:"bearer_token_env_var"`
	Enabled        *bool             `toml:"enabled"`
	Disabled       bool              `json:"disabled"`
	HTTPURL        string            `json:"httpUrl"` // Gemini's streamable HTTP address; its "url" is SSE
}

// Load returns the MCP servers the user configured for agent ("claude",
// "codex", "agy", "grok", "cursor", "gemini", "opencode", "cline",
// "muse-code", "kilo", "goose" or "amp-acp"), keyed by name. cwd adds Claude's
// servers for that project. Errors name only the file, because its content can
// hold secrets.
func Load(home, agent, cwd string) (map[string]server, error) {
	var path string
	switch agent {
	case "claude":
		path = filepath.Join(home, ".claude.json")
	case "agy":
		path = filepath.Join(home, ".gemini", "config", "mcp_config.json")
	case "codex":
		path = filepath.Join(home, ".codex", "config.toml")
	case "grok":
		path = filepath.Join(home, ".grok", "config.toml")
	case "cursor":
		path = filepath.Join(home, ".cursor", "mcp.json")
	case "gemini":
		path = filepath.Join(home, ".gemini", "settings.json")
	case "cline":
		path = filepath.Join(home, ".cline", "data", "settings", "cline_mcp_settings.json")
	case "muse-code":
		path = filepath.Join(home, ".config", "muse", "settings.json")
	case "opencode":
		return loadOpenCode(filepath.Join(home, ".config", "opencode"), "opencode")
	case "kilo":
		return loadOpenCode(filepath.Join(home, ".config", "kilo"), "opencode", "kilo")
	case "goose":
		return loadGoose(filepath.Join(home, ".config", "goose", "config.yaml"))
	case "amp-acp":
		return loadAmp(filepath.Join(home, ".config", "amp"))
	default:
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers  map[string]server `json:"mcpServers" toml:"mcp_servers"`
		Muse     map[string]server `json:"mcp_servers" toml:"-"`
		Projects map[string]struct {
			Servers map[string]server `json:"mcpServers"`
		} `json:"projects"`
	}
	if filepath.Ext(path) == ".toml" {
		_, err = toml.Decode(string(data), &file)
	} else {
		err = json.Unmarshal(data, &file)
	}
	if err != nil {
		return nil, fmt.Errorf("can't read MCP servers: %s is not valid", path)
	}
	out := make(map[string]server)
	maps.Copy(out, file.Servers)
	if agent == "muse-code" {
		maps.Copy(out, file.Muse)
	}
	maps.Copy(out, file.Projects[cwd].Servers)
	for name, s := range out {
		if agent == "gemini" {
			if s.HTTPURL != "" {
				s.URL = s.HTTPURL
			} else if s.URL != "" {
				s.Type = "sse"
			}
			out[name] = s
		}
		// Cline's remote servers use SSE unless they name streamable HTTP.
		if agent == "cline" && s.URL != "" && s.Type != "streamableHttp" {
			s.Type = "sse"
			out[name] = s
		}
		if s.Disabled || (s.Enabled != nil && !*s.Enabled) {
			delete(out, name)
		}
	}
	return out, nil
}

// Merge appends servers to the ACP mcpServers list the host sent, skipping
// names the host already uses and transports the agent doesn't accept.
func Merge(sent []any, servers map[string]server, caps Capabilities) []any {
	taken := make(map[string]bool)
	for _, s := range sent {
		if m, ok := s.(map[string]any); ok {
			name, _ := m["name"].(string)
			taken[name] = true
		}
	}
	out := slices.Clone(sent)
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if taken[name] {
			continue
		}
		s := servers[name]
		if url := cmp.Or(s.URL, s.ServerURL); url != "" {
			transport := "http"
			if s.Type == "sse" {
				transport = "sse"
			}
			if (transport == "http" && !caps.HTTP) || (transport == "sse" && !caps.SSE) {
				continue
			}
			headers := maps.Clone(s.Headers)
			if token := os.Getenv(s.BearerTokenEnv); s.BearerTokenEnv != "" && token != "" {
				if headers == nil {
					headers = map[string]string{}
				}
				headers["Authorization"] = "Bearer " + token
			}
			out = append(out, map[string]any{"type": transport, "name": name, "url": url, "headers": pairs(headers)})
			continue
		}
		if s.Command == "" {
			continue
		}
		command := s.Command
		if path, err := exec.LookPath(command); err == nil {
			command = path
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		out = append(out, map[string]any{"name": name, "command": command, "args": args, "env": pairs(userEnv(s.Env))})
	}
	return out
}

// userEnv restores the user's own home for a server, which would otherwise
// inherit the agent's isolated one and lose its settings and sign-ins.
func userEnv(configured map[string]string) map[string]string {
	env := make(map[string]string)
	if home, err := os.UserHomeDir(); err == nil {
		env["HOME"] = home
		env["XDG_CONFIG_HOME"] = cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	}
	if bus := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); bus != "" {
		env["DBUS_SESSION_BUS_ADDRESS"] = bus
	}
	maps.Copy(env, configured)
	return env
}

func pairs(m map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, map[string]string{"name": k, "value": m[k]})
	}
	return out
}
