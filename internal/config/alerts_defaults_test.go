package config

import (
	"os"
	"testing"
)

func TestAlertDefaultsAndExplicitOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte("[ui]\ntheme = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := AlertsConfig{Enabled: true, Desktop: true, QuotaThresholdPct: 90, BalanceThreshold: 2, ReminderHours: 24}
	if cfg.Alerts != want {
		t.Fatalf("a config without [alerts] got %+v, want %+v", cfg.Alerts, want)
	}

	cfg.Alerts = AlertsConfig{}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts != (AlertsConfig{}) {
		t.Fatalf("alerts turned off came back as %+v", cfg.Alerts)
	}
}
