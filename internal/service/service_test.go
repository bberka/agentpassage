package service

import (
	"strings"
	"testing"
)

func TestRenderSystemdAndLaunchdUnits(t *testing.T) {
	unit := RenderSystemdUnit("/home/user/.local/bin/apx", "/home/user/.config/apx", "/usr/local/bin:/usr/bin")
	for _, want := range []string{
		"ExecStart=/home/user/.local/bin/apx serve",
		"Restart=on-failure",
		`Environment="PATH=/usr/local/bin:/usr/bin"`,
		`Environment="APX_HOME=/home/user/.config/apx"`,
		"PassEnvironment=HOME DBUS_SESSION_BUS_ADDRESS",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, unit)
		}
	}

	plist := RenderLaunchdPlist("/usr/local/bin/apx", "/Users/u/Library/Application Support/apx", "/usr/local/bin:/usr/bin", true)
	for _, want := range []string{
		"<string>com.bberka.agentpassage</string>",
		"<string>/usr/local/bin/apx</string>",
		"<string>serve</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("launchd plist missing %q:\n%s", want, plist)
		}
	}

	plistOff := RenderLaunchdPlist("/usr/local/bin/apx", "/Users/u/Library/Application Support/apx", "/usr/local/bin:/usr/bin", false)
	if !strings.Contains(plistOff, "<key>RunAtLoad</key>\n  <false/>") {
		t.Fatalf("expected RunAtLoad false when autoStart=false:\n%s", plistOff)
	}
}
