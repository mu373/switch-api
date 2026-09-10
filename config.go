package main

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddr    = ":8010"
	defaultActionTimeout = 90 * time.Second
	maxActionTimeout     = 10 * time.Minute
	maxStepDelay         = 5 * time.Minute
	defaultSwitchBotURL  = "https://api.switch-bot.com/v1.1"
)

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

type config struct {
	ListenAddr     string                        `json:"listen_addr" yaml:"listen_addr"`
	ActionTimeout  string                        `json:"action_timeout" yaml:"action_timeout"`
	SwitchBot      *switchBotConfig              `json:"switchbot,omitempty" yaml:"switchbot,omitempty"`
	ShellyDevices  map[string]shellyDeviceConfig `json:"shelly_devices,omitempty" yaml:"shelly_devices,omitempty"`
	Switches       []logicalSwitchConfig         `json:"switches" yaml:"switches"`
	actionDuration time.Duration
}

type switchBotConfig struct {
	BaseURL   string `json:"base_url" yaml:"base_url"`
	TokenEnv  string `json:"token_env" yaml:"token_env"`
	SecretEnv string `json:"secret_env" yaml:"secret_env"`
}

type shellyDeviceConfig struct {
	BaseURL     string `json:"base_url" yaml:"base_url"`
	ComponentID int    `json:"component_id" yaml:"component_id"`
}

type logicalSwitchConfig struct {
	ID          string       `json:"id" yaml:"id"`
	DisplayName string       `json:"display_name" yaml:"display_name"`
	On          []stepConfig `json:"on" yaml:"on"`
	Off         []stepConfig `json:"off" yaml:"off"`
}

type stepConfig struct {
	Driver   string `json:"driver" yaml:"driver"`
	DeviceID string `json:"device_id,omitempty" yaml:"device_id,omitempty"`
	Action   string `json:"action,omitempty" yaml:"action,omitempty"`
	Duration string `json:"duration,omitempty" yaml:"duration,omitempty"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}

	var cfg config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := ensureConfigEOF(decoder); err != nil {
		return config{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return config{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return cfg, nil
}

func ensureConfigEOF(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple configuration documents are not allowed")
	}
	return err
}

func (c *config) validate() error {
	if c.ListenAddr == "" {
		c.ListenAddr = defaultListenAddr
	}
	if c.ActionTimeout == "" {
		c.actionDuration = defaultActionTimeout
		c.ActionTimeout = defaultActionTimeout.String()
	} else {
		duration, err := time.ParseDuration(c.ActionTimeout)
		if err != nil || duration <= 0 || duration > maxActionTimeout {
			return fmt.Errorf("action_timeout must be a duration between 1ns and %s", maxActionTimeout)
		}
		c.actionDuration = duration
	}

	if c.SwitchBot != nil {
		if err := c.SwitchBot.validate(); err != nil {
			return fmt.Errorf("switchbot: %w", err)
		}
	}
	for id, device := range c.ShellyDevices {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("invalid shelly device id %q", id)
		}
		if err := device.validate(); err != nil {
			return fmt.Errorf("shelly_devices[%q]: %w", id, err)
		}
	}

	if len(c.Switches) == 0 {
		return fmt.Errorf("at least one switch is required")
	}
	seen := make(map[string]struct{}, len(c.Switches))
	for i := range c.Switches {
		switchConfig := &c.Switches[i]
		if err := switchConfig.validate(c); err != nil {
			return fmt.Errorf("switches[%d]: %w", i, err)
		}
		if _, ok := seen[switchConfig.ID]; ok {
			return fmt.Errorf("duplicate switch id %q", switchConfig.ID)
		}
		seen[switchConfig.ID] = struct{}{}
	}
	return nil
}

func (c *switchBotConfig) validate() error {
	if c.BaseURL == "" {
		c.BaseURL = defaultSwitchBotURL
	}
	if err := validateBaseURL(c.BaseURL, true); err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if c.TokenEnv == "" {
		c.TokenEnv = "SWITCHBOT_TOKEN"
	}
	if c.SecretEnv == "" {
		c.SecretEnv = "SWITCHBOT_SECRET"
	}
	if !envPattern.MatchString(c.TokenEnv) {
		return fmt.Errorf("invalid token_env %q", c.TokenEnv)
	}
	if !envPattern.MatchString(c.SecretEnv) {
		return fmt.Errorf("invalid secret_env %q", c.SecretEnv)
	}
	return nil
}

func (c shellyDeviceConfig) validate() error {
	if err := validateBaseURL(c.BaseURL, false); err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if c.ComponentID < 0 {
		return fmt.Errorf("component_id must not be negative")
	}
	return nil
}

func validateBaseURL(value string, requireHTTPS bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if requireHTTPS && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("query and fragment are not allowed")
	}
	return nil
}

func (c *logicalSwitchConfig) validate(root *config) error {
	if !idPattern.MatchString(c.ID) {
		return fmt.Errorf("invalid id %q", c.ID)
	}
	if c.DisplayName == "" {
		c.DisplayName = c.ID
	}
	if len(c.On) == 0 || len(c.Off) == 0 {
		return fmt.Errorf("on and off must each contain at least one step")
	}
	for name, steps := range map[string][]stepConfig{"on": c.On, "off": c.Off} {
		for i := range steps {
			if err := steps[i].validate(root); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

func (s stepConfig) validate(root *config) error {
	switch s.Driver {
	case "switchbot":
		if s.DeviceID == "" || strings.ContainsAny(s.DeviceID, "\x00\r\n/") {
			return fmt.Errorf("switchbot device_id is required and must not contain slashes")
		}
		if s.Action != "turnOn" && s.Action != "turnOff" && s.Action != "press" {
			return fmt.Errorf("switchbot action must be turnOn, turnOff, or press")
		}
		if s.Duration != "" {
			return fmt.Errorf("duration is only valid for delay steps")
		}
		if root.SwitchBot == nil && !s.Optional {
			return fmt.Errorf("switchbot is not configured; mark the step optional to allow it")
		}
	case "shelly-gen2":
		if !idPattern.MatchString(s.DeviceID) {
			return fmt.Errorf("invalid shelly device_id %q", s.DeviceID)
		}
		if s.Action != "on" && s.Action != "off" {
			return fmt.Errorf("shelly-gen2 action must be on or off")
		}
		if s.Duration != "" {
			return fmt.Errorf("duration is only valid for delay steps")
		}
		if _, ok := root.ShellyDevices[s.DeviceID]; !ok && !s.Optional {
			return fmt.Errorf("shelly device %q is not configured; mark the step optional to allow it", s.DeviceID)
		}
	case "delay":
		if s.DeviceID != "" || s.Action != "" || s.Optional {
			return fmt.Errorf("delay accepts only driver and duration")
		}
		duration, err := time.ParseDuration(s.Duration)
		if err != nil || duration <= 0 || duration > maxStepDelay {
			return fmt.Errorf("duration must be between 1ns and %s", maxStepDelay)
		}
	default:
		return fmt.Errorf("unknown driver %q", s.Driver)
	}
	return nil
}
