package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigAllowsMissingOptionalShellyDevice(t *testing.T) {
	path := writeTestConfig(t, `{
  "switchbot": {},
  "switches": [{
    "id": "printer",
    "on": [
      {"driver":"shelly-gen2","device_id":"plug","action":"on","optional":true},
      {"driver":"switchbot","device_id":"bot","action":"turnOn"}
    ],
    "off": [{"driver":"switchbot","device_id":"bot","action":"turnOff"}]
  }]
}`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got, want := cfg.ListenAddr, ":8010"; got != want {
		t.Fatalf("ListenAddr = %q, want %q", got, want)
	}
	if got, want := cfg.Switches[0].DisplayName, "printer"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
}

func TestLoadConfigRejectsMissingRequiredShellyDevice(t *testing.T) {
	path := writeTestConfig(t, `{
  "switches": [{
    "id": "printer",
    "on": [{"driver":"shelly-gen2","device_id":"plug","action":"on"}],
    "off": [{"driver":"shelly-gen2","device_id":"plug","action":"off"}]
  }]
}`)
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("loadConfig() error = %v, want missing device error", err)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	path := writeTestConfig(t, `{"unknown":true,"switches":[]}`)
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("loadConfig() error = %v, want unknown field error", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := loadConfig("config.example.yaml"); err != nil {
		t.Fatalf("loadConfig(config.example.yaml) error = %v", err)
	}
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
