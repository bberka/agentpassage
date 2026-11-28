package clibackend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/hostmcp"
	"github.com/bberka/agentpassage/internal/sharing"
)

// hostMCP gives one CLI run the MCP servers from the user's own agent config. It
// returns the arguments to put after the binary name, the variables to add, and
// a cleanup to call once the run ends. Nothing it writes is read by a later run,
// so API requests, which share the profile, never get these servers. A failure
// is logged and the run goes on without them.
func (m *Manager) hostMCP(acct config.AccountConfig, profileDir, cwd string) (args, env []string, cleanup func()) {
	cleanup = func() {}
	warn := func(err error) {
		if m.logs != nil {
			m.logs.Warn("cli", fmt.Sprintf("share MCP servers with %s/%s: %v", acct.Backend, acct.Name, err))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		warn(err)
		return
	}
	servers, err := hostmcp.Load(home, acct.Backend, cwd)
	if err != nil {
		warn(err)
		return
	}
	// Unreadable choices allow nothing.
	choices, err := sharing.Load(m.dataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		warn(err)
	}
	data, err := hostmcp.CLIConfig(acct.Backend, hostmcp.Keep(servers, choices.Allows))
	if err != nil {
		warn(err)
		return
	}
	if data == nil {
		return
	}
	if acct.Backend == "opencode" {
		return nil, []string{"OPENCODE_CONFIG_CONTENT=" + string(data)}, cleanup
	}
	// A file, because the servers can hold secrets that arguments would expose.
	suffix := ".json"
	if acct.Backend == "codex" {
		suffix = ".config.toml"
	}
	f, err := os.CreateTemp(profileDir, "apx-mcp-*"+suffix)
	if err != nil {
		warn(err)
		return
	}
	remove := func() { _ = os.Remove(f.Name()) }
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		remove()
		warn(err)
		return
	}
	if acct.Backend == "codex" {
		return []string{"-p", strings.TrimSuffix(filepath.Base(f.Name()), suffix)}, nil, remove
	}
	return []string{"--mcp-config", f.Name()}, nil, remove
}
