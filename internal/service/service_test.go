package service

import (
	"strings"
	"testing"
)

func TestRenderSystemdAndLaunchdUnits(t *testing.T) {
	unit := RenderSystemdUnit("/home/user/.local/bin/acpx", "/home/user/.config/acpx", "/usr/local/bin:/usr/bin")
	for _, want := range []string{
		"ExecStart=/home/user/.local/bin/acpx serve",
		"Restart=on-failure",
		`Environment="PATH=/usr/local/bin:/usr/bin"`,
		`Environment="ACPX_HOME=/home/user/.config/acpx"`,
		"PassEnvironment=HOME DBUS_SESSION_BUS_ADDRESS",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, unit)
		}
	}

	plist := RenderLaunchdPlist("/usr/local/bin/acpx", "/Users/u/Library/Application Support/acpx", "/usr/local/bin:/usr/bin", true)
	for _, want := range []string{
		"<string>com.bberka.acpx</string>",
		"<string>/usr/local/bin/acpx</string>",
		"<string>serve</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("launchd plist missing %q:\n%s", want, plist)
		}
	}

	plistOff := RenderLaunchdPlist("/usr/local/bin/acpx", "/Users/u/Library/Application Support/acpx", "/usr/local/bin:/usr/bin", false)
	if !strings.Contains(plistOff, "<key>RunAtLoad</key>\n  <false/>") {
		t.Fatalf("expected RunAtLoad false when autoStart=false:\n%s", plistOff)
	}
}
