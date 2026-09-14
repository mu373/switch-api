package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type deviceReference struct {
	Driver   string `json:"driver" yaml:"driver"`
	DeviceID string `json:"device_id" yaml:"device_id"`
}

type bodyPowerConfig struct {
	Driver     string `json:"driver" yaml:"driver"`
	DeviceID   string `json:"device_id" yaml:"device_id"`
	OnCommand  string `json:"on_command,omitempty" yaml:"on_command,omitempty"`
	OffCommand string `json:"off_command,omitempty" yaml:"off_command,omitempty"`
}

type ippPrinterConfig struct {
	IPPURI                                                             string           `json:"ipp_uri" yaml:"ipp_uri"`
	Supply                                                             deviceReference  `json:"supply" yaml:"supply"`
	PowerControl                                                       *bodyPowerConfig `json:"power_control,omitempty" yaml:"power_control,omitempty"`
	AutoStartWait                                                      string           `json:"auto_start_wait,omitempty" yaml:"auto_start_wait,omitempty"`
	StartupTimeout                                                     string           `json:"startup_timeout,omitempty" yaml:"startup_timeout,omitempty"`
	ShutdownWait                                                       string           `json:"shutdown_wait,omitempty" yaml:"shutdown_wait,omitempty"`
	PollInterval                                                       string           `json:"poll_interval,omitempty" yaml:"poll_interval,omitempty"`
	autoStartDuration, startupDuration, shutdownDuration, pollDuration time.Duration
}

func (c *config) validatePrinterBindings() error {
	resources := map[deviceReference]string{}
	endpoints := map[string]string{}
	for id, printer := range c.IPPPrinters {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("invalid IPP printer id %q", id)
		}
		if err := printer.validate(c); err != nil {
			return fmt.Errorf("ipp_printers[%q]: %w", id, err)
		}
		c.IPPPrinters[id] = printer
		if owner, ok := endpoints[printer.IPPURI]; ok {
			return fmt.Errorf("IPP printers %q and %q share an endpoint; use logical aliases instead", owner, id)
		}
		endpoints[printer.IPPURI] = id
		bindings := []deviceReference{printer.Supply}
		if printer.PowerControl != nil {
			bindings = append(bindings, deviceReference{printer.PowerControl.Driver, printer.PowerControl.DeviceID})
		}
		for _, binding := range bindings {
			binding = c.resolvePhysicalReference(binding)
			if owner, ok := resources[binding]; ok {
				return fmt.Errorf("IPP printers %q and %q share a physical device; use logical aliases instead", owner, id)
			}
			resources[binding] = id
		}
	}
	for _, logical := range c.Switches {
		for _, steps := range [][]stepConfig{logical.On, logical.Off} {
			for _, step := range steps {
				if owner, ok := resources[c.resolvePhysicalReference(deviceReference{step.Driver, step.DeviceID})]; ok {
					return fmt.Errorf("switch %q bypasses physical device owned by IPP printer %q", logical.ID, owner)
				}
			}
		}
	}
	return nil
}

func (c config) resolvePhysicalReference(reference deviceReference) deviceReference {
	if reference.Driver == "shelly-gen2" {
		if device, ok := c.ShellyDevices[reference.DeviceID]; ok {
			reference.DeviceID = fmt.Sprintf("%s#%d", strings.TrimRight(device.BaseURL, "/"), device.ComponentID)
		}
	}
	return reference
}

func (p *ippPrinterConfig) validate(root *config) error {
	uri, err := url.Parse(p.IPPURI)
	if err != nil || (uri.Scheme != "ipp" && uri.Scheme != "ipps") || uri.Host == "" || uri.User != nil || uri.RawQuery != "" || uri.Fragment != "" || len(p.IPPURI) > 32767 {
		return fmt.Errorf("ipp_uri must be an absolute IPP URL without credentials, query, or fragment")
	}
	if p.Supply.Driver != "shelly-gen2" {
		return fmt.Errorf("unsupported supply driver %q", p.Supply.Driver)
	}
	if _, ok := root.ShellyDevices[p.Supply.DeviceID]; !ok {
		return fmt.Errorf("supply device %q is not configured", p.Supply.DeviceID)
	}
	if p.PowerControl != nil {
		control := p.PowerControl
		if control.Driver != "switchbot" || root.SwitchBot == nil {
			return fmt.Errorf("power_control requires a configured switchbot provider")
		}
		if control.DeviceID == "" || strings.ContainsAny(control.DeviceID, "\x00\r\n/") {
			return fmt.Errorf("invalid power_control device_id")
		}
		if control.OnCommand == "" {
			control.OnCommand = "turnOn"
		}
		if control.OffCommand == "" {
			control.OffCommand = "turnOff"
		}
		for _, command := range []string{control.OnCommand, control.OffCommand} {
			if command != "turnOn" && command != "turnOff" && command != "press" {
				return fmt.Errorf("unsupported power_control command")
			}
		}
	}
	for _, option := range []struct {
		value     *string
		parsed    *time.Duration
		fallback  string
		allowZero bool
	}{
		{&p.AutoStartWait, &p.autoStartDuration, "30s", true},
		{&p.StartupTimeout, &p.startupDuration, "60s", false},
		{&p.ShutdownWait, &p.shutdownDuration, "15s", true},
		{&p.PollInterval, &p.pollDuration, "1s", false},
	} {
		if *option.value == "" {
			*option.value = option.fallback
		}
		duration, err := time.ParseDuration(*option.value)
		if err != nil || duration < 0 || (!option.allowZero && duration == 0) || duration > maxStepDelay {
			return fmt.Errorf("invalid printer timing %q", *option.value)
		}
		*option.parsed = duration
	}
	if p.autoStartDuration >= p.startupDuration || p.pollDuration > p.startupDuration {
		return fmt.Errorf("auto_start_wait and poll_interval must fit within startup_timeout")
	}
	// Leave ten seconds inside the overall action budget for supply cleanup.
	if p.startupDuration+10*time.Second > root.actionDuration || p.shutdownDuration+switchBotStepTimeout+10*time.Second > root.actionDuration {
		return fmt.Errorf("printer timings leave insufficient action_timeout for supply cleanup")
	}
	return nil
}
