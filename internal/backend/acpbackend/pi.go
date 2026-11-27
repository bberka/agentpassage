package acpbackend

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// piMCPEnv gives Pi the user's MCP servers. Pi's adapter drops the ones sent
// over ACP, so the user's mcp.json is linked into the profile. The profile also
// serves API requests, whose Pi is therefore started with --no-mcp through a
// small launcher.
func piMCPEnv(profileDir, command string, hostTools bool) ([]string, error) {
	if hostTools {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		source := filepath.Join(home, ".pi", "agent", "mcp.json")
		link := filepath.Join(profileDir, "mcp.json")
		if _, err := os.Stat(source); err != nil {
			return nil, nil
		}
		// A file the account already has is never replaced.
		if _, err := os.Lstat(link); err == nil {
			return nil, nil
		}
		if err := os.Symlink(source, link); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("share MCP servers with pi: %w", err)
		}
		return nil, nil
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
