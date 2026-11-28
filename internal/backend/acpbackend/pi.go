package acpbackend

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bberka/agentpassage/internal/hostmcp"
)

// piMCPEnv gives Pi the user's approved MCP servers. Pi's adapter drops the
// ones sent over ACP, so they go into the profile's mcp.json. The profile also
// serves API requests, whose Pi is therefore started with --no-mcp through a
// small launcher.
func piMCPEnv(profileDir, command string, hostTools bool, allow func(name, fingerprint string) bool) ([]string, error) {
	if hostTools {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		return nil, writePiMCP(home, profileDir, allow)
	}
	command = cmp.Or(command, "pi")
	name, script := "pi-no-mcp", "#!/bin/sh\nexec '"+strings.ReplaceAll(command, "'", `'\''`)+"' --no-mcp \"$@\"\n"
	if runtime.GOOS == "windows" {
		name, script = "pi-no-mcp.cmd", "@echo off\r\n\""+command+"\" --no-mcp %*\r\n"
	}
	path := filepath.Join(profileDir, name)
	// Written aside and renamed, because another apx process may be running it.
	tmp, err := os.CreateTemp(profileDir, name+".*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(script)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o700)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return nil, fmt.Errorf("prepare pi without MCP: %w", err)
	}
	return []string{"PI_ACP_PI_COMMAND=" + path}, nil
}

// dropWorkerUnless closes the account's process when it was started with the
// other MCP setting, so the next one starts with the right one.
func (m *Manager) dropWorkerUnless(accountID string, hostTools bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.workers[accountID]; ok && w.hostTools != hostTools {
		w.close()
		delete(m.workers, accountID)
	}
}

// writePiMCP gives the profile a copy of the user's mcp.json holding only the
// approved servers. A file the account made itself is never replaced: apx
// replaces only its own copy, marked by a second file, or an earlier link.
func writePiMCP(home, profileDir string, allow func(name, fingerprint string) bool) error {
	source := filepath.Join(home, ".pi", "agent", "mcp.json")
	dest := filepath.Join(profileDir, "mcp.json")
	marker := dest + ".apx"
	if info, err := os.Lstat(dest); err == nil {
		target, _ := os.Readlink(dest)
		_, marked := os.Stat(marker)
		if !(info.Mode()&os.ModeSymlink != 0 && target == source) && marked != nil {
			return nil
		}
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("share MCP servers with pi: %w", err)
		}
	}
	data, err := os.ReadFile(source)
	if os.IsNotExist(err) {
		return nil
	}
	servers, loadErr := hostmcp.Load(home, "pi", "")
	if err = cmp.Or(err, loadErr); err != nil {
		return fmt.Errorf("share MCP servers with pi: %w", err)
	}
	approved := hostmcp.Keep(servers, allow)
	var file, all map[string]json.RawMessage
	if json.Unmarshal(data, &file) != nil || (file["mcpServers"] != nil && json.Unmarshal(file["mcpServers"], &all) != nil) {
		return fmt.Errorf("share MCP servers with pi: %s is not valid", source)
	}
	for name := range all {
		if _, ok := approved[name]; !ok {
			delete(all, name)
		}
	}
	if file["mcpServers"], err = json.Marshal(all); err != nil {
		return err
	}
	if data, err = json.Marshal(file); err != nil {
		return err
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return fmt.Errorf("share MCP servers with pi: %w", err)
	}
	return os.WriteFile(dest, data, 0o600)
}
