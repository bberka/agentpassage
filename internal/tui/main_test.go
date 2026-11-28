package tui

import (
	"os"
	"testing"
)

// Tests must not read the user's own agent configs, which could announce their
// MCP servers as desktop notifications.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "apx-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
