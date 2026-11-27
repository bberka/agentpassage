package hostmcp

import (
	"bytes"
	"encoding/json"

	"github.com/BurntSushi/toml"
)

// CLIConfig writes servers in the format agent's CLI accepts for a single run:
// a "claude" --mcp-config file, a "codex" profile file, or the content of
// OPENCODE_CONFIG_CONTENT for "opencode". It returns nil for any other agent,
// and when there is no server the agent can use.
func CLIConfig(agent string, servers map[string]server) ([]byte, error) {
	if agent != "claude" && agent != "codex" && agent != "opencode" {
		return nil, nil
	}
	entries := make(map[string]any)
	// Codex has no SSE transport.
	for _, raw := range Merge(nil, servers, Capabilities{HTTP: true, SSE: agent != "codex"}) {
		s := raw.(map[string]any)
		name := s["name"].(string)
		entry := map[string]any{}
		if url, remote := s["url"]; remote {
			entry["url"] = url
			headers := values(s["headers"])
			switch agent {
			case "claude":
				entry["type"], entry["headers"] = s["type"], headers
			case "codex":
				entry["http_headers"] = headers
			case "opencode":
				entry["type"], entry["headers"] = "remote", headers
			}
		} else if agent == "opencode" {
			entry["type"] = "local"
			entry["command"] = append([]string{s["command"].(string)}, s["args"].([]string)...)
			entry["environment"] = values(s["env"])
		} else {
			entry["command"], entry["args"], entry["env"] = s["command"], s["args"], values(s["env"])
		}
		entries[name] = entry
	}
	if len(entries) == 0 {
		return nil, nil
	}
	switch agent {
	case "codex":
		var buf bytes.Buffer
		err := toml.NewEncoder(&buf).Encode(map[string]any{"mcp_servers": entries})
		return buf.Bytes(), err
	case "opencode":
		return json.Marshal(map[string]any{"mcp": map[string]any{"servers": entries}})
	}
	return json.Marshal(map[string]any{"mcpServers": entries})
}

// values turns ACP name and value pairs back into a map.
func values(v any) map[string]string {
	out := make(map[string]string)
	for _, p := range v.([]map[string]string) {
		out[p["name"]] = p["value"]
	}
	return out
}
