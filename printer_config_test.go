package main

import (
	"strings"
	"testing"
)

func createPrinterConfig() config {
	return config{
		SwitchBot:     &switchBotConfig{},
		ShellyDevices: map[string]shellyDeviceConfig{"plug": {BaseURL: "http://127.0.0.1", ComponentID: 0}},
		IPPPrinters: map[string]ippPrinterConfig{"printer": {
			IPPURI: "ipp://127.0.0.2:631/ipp/print", Supply: deviceReference{"shelly-gen2", "plug"}, PowerControl: &bodyPowerConfig{Driver: "switchbot", DeviceID: "BOT"},
		}},
		Switches: []logicalSwitchConfig{{ID: "printer", Driver: "ipp-printer", DeviceID: "printer"}},
	}
}

func TestIPPPrinterExampleSupportsMultipleDevices(t *testing.T) {
	cfg, err := loadConfig("config.ipp-printers.example.yaml")
	if err != nil || len(cfg.IPPPrinters) != 2 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestPrinterConfigNormalizesTimingAndCommands(t *testing.T) {
	cfg := createPrinterConfig()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	p := cfg.IPPPrinters["printer"]
	if p.StartupTimeout != "60s" || p.AutoStartWait != "30s" || p.ShutdownWait != "15s" || p.PowerControl.OnCommand != "turnOn" {
		t.Fatalf("defaults=%+v", p)
	}
	cfg.Switches = append(cfg.Switches, logicalSwitchConfig{ID: "alias", Driver: "ipp-printer", DeviceID: "printer"})
	if err := cfg.validate(); err != nil {
		t.Fatalf("logical alias rejected: %v", err)
	}
}

func TestPrinterConfigRejectsInvalidBindingsAndTimings(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config)
		want   string
	}{
		{"missing supply", func(c *config) {
			p := c.IPPPrinters["printer"]
			p.Supply.DeviceID = "missing"
			c.IPPPrinters["printer"] = p
		}, "not configured"},
		{"unsupported supply", func(c *config) {
			p := c.IPPPrinters["printer"]
			p.Supply.Driver = "unimplemented"
			c.IPPPrinters["printer"] = p
		}, "unsupported supply"},
		{"unsupported body", func(c *config) { c.IPPPrinters["printer"].PowerControl.Driver = "unimplemented" }, "power_control"},
		{"unknown printer", func(c *config) { c.Switches[0].DeviceID = "missing" }, "not configured"},
		{"steps and controller", func(c *config) { c.Switches[0].On = []stepConfig{{Driver: "delay", Duration: "1s"}} }, "cannot also define"},
		{"shared supply", func(c *config) {
			p := c.IPPPrinters["printer"]
			p.IPPURI = "ipp://127.0.0.3/ipp/print"
			p.PowerControl = nil
			c.IPPPrinters["second"] = p
		}, "share a physical"},
		{"legacy bypass", func(c *config) {
			c.Switches = append(c.Switches, logicalSwitchConfig{ID: "bypass", On: []stepConfig{{Driver: "shelly-gen2", DeviceID: "plug", Action: "on"}}})
		}, "bypasses"},
		{"zero poll", func(c *config) { p := c.IPPPrinters["printer"]; p.PollInterval = "0s"; c.IPPPrinters["printer"] = p }, "invalid printer timing"},
		{"negative grace", func(c *config) { p := c.IPPPrinters["printer"]; p.ShutdownWait = "-1s"; c.IPPPrinters["printer"] = p }, "invalid printer timing"},
		{"auto wait exceeds startup", func(c *config) { p := c.IPPPrinters["printer"]; p.AutoStartWait = "60s"; c.IPPPrinters["printer"] = p }, "fit within"},
		{"no cleanup budget", func(c *config) { c.ActionTimeout = "60s" }, "insufficient"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := createPrinterConfig()
			test.mutate(&cfg)
			if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
		})
	}
}

func TestPrinterConfigRejectsInvalidIPPURI(t *testing.T) {
	for _, uri := range []string{"http://printer/ipp/print", "ipp:///ipp/print", "ipp://user:password@printer/ipp/print", "ipp://printer/ipp/print?key=value", "ipp://printer/ipp/print#fragment"} {
		cfg := createPrinterConfig()
		p := cfg.IPPPrinters["printer"]
		p.IPPURI = uri
		cfg.IPPPrinters["printer"] = p
		if err := cfg.validate(); err == nil {
			t.Fatalf("invalid URI accepted %q", uri)
		}
	}
}

func TestPrinterConfigRejectsSupplyAliasesPointingToSameRelay(t *testing.T) {
	cfg := createPrinterConfig()
	cfg.ShellyDevices["alias"] = cfg.ShellyDevices["plug"]
	p := cfg.IPPPrinters["printer"]
	p.IPPURI = "ipp://127.0.0.3/ipp/print"
	p.Supply.DeviceID = "alias"
	p.PowerControl = nil
	cfg.IPPPrinters["second"] = p
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "share a physical") {
		t.Fatalf("unsafe supply alias accepted: %v", err)
	}
}

func TestRemovedControlLanguageFieldsAreRejected(t *testing.T) {
	for _, field := range []string{`"when_power":{"device_id":"plug","state":"off"}`, `"continue_on_error":true`} {
		path := writeTestConfig(t, `{"switches":[{"id":"printer","on":[{"driver":"switchbot","device_id":"BOT","action":"turnOn",`+field+`}]}]}`)
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "field") {
			t.Fatalf("old DSL field accepted: %v", err)
		}
	}
}
