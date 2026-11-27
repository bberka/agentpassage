package tui

import (
	"testing"

	"github.com/bberka/agentpassage/internal/config"
)

func TestOrZeroAndAlertDestination(t *testing.T) {
	if orZero("  ") != "0" || orZero(" 5 ") != "5" {
		t.Fatal("blank input must mean 0")
	}
	c := &config.Config{}
	enableAlertDestination(c)
	if !c.Alerts.Enabled || !c.Alerts.Desktop {
		t.Fatalf("want enabled desktop alerts, got %+v", c.Alerts)
	}
	c = &config.Config{Alerts: config.AlertsConfig{WebhookURL: "http://example.invalid"}}
	enableAlertDestination(c)
	if c.Alerts.Desktop {
		t.Fatal("a webhook is already a destination")
	}
}
