package main

import (
	"context"
	"fmt"
	"math"
	"time"
)

type shellySupply struct {
	client    *shellyClient
	providers *providers
}

func (s *shellySupply) setSupply(ctx context.Context, output bool) error {
	if err := s.client.set(ctx, output); err != nil {
		return err
	}
	_, err := s.providers.verifyShellyOutput(ctx, s.client, output)
	return err
}

func (s *shellySupply) readSupply(ctx context.Context) (supplyObservation, error) {
	status, err := s.client.readStatus(ctx)
	if err != nil {
		return supplyObservation{}, err
	}
	if len(status.Errors) != 0 {
		return supplyObservation{}, fmt.Errorf("supply reports device errors")
	}
	if status.PowerWatts != nil && (*status.PowerWatts < 0 || math.IsNaN(*status.PowerWatts) || math.IsInf(*status.PowerWatts, 0)) {
		return supplyObservation{}, fmt.Errorf("supply reports invalid measured watts")
	}
	return supplyObservation{Output: *status.Output, PowerWatts: status.PowerWatts}, nil
}

type switchBotPowerControl struct {
	client *switchBotClient
	config bodyPowerConfig
}

func (c *switchBotPowerControl) turnOn(ctx context.Context) error {
	return c.sendCommand(ctx, c.config.OnCommand)
}
func (c *switchBotPowerControl) turnOff(ctx context.Context) error {
	return c.sendCommand(ctx, c.config.OffCommand)
}
func (c *switchBotPowerControl) sendCommand(parent context.Context, command string) error {
	ctx, cancel := context.WithTimeout(parent, switchBotStepTimeout)
	defer cancel()
	return c.client.command(ctx, c.config.DeviceID, command)
}

type ippDeviceProbe struct {
	client *ippClient
	uri    string
}

func (p *ippDeviceProbe) readDeviceState(ctx context.Context) (string, error) {
	responsive, err := p.client.readResponse(ctx, p.uri)
	if err != nil {
		return powerUnknown, err
	}
	if responsive {
		return "on", nil
	}
	return powerUnknown, nil
}

// Only wiring knows concrete providers. The controller consumes capabilities.
func (p *providers) buildPrinterController(id string, cfg ippPrinterConfig, client *ippClient) (*printerController, error) {
	var supply powerSupply
	switch cfg.Supply.Driver {
	case "shelly-gen2":
		device, ok := p.shelly[cfg.Supply.DeviceID]
		if !ok {
			return nil, fmt.Errorf("printer supply is not configured")
		}
		supply = &shellySupply{client: device, providers: p}
	default:
		return nil, fmt.Errorf("unsupported printer supply driver")
	}
	var body devicePowerControl
	if cfg.PowerControl != nil {
		switch cfg.PowerControl.Driver {
		case "switchbot":
			if p.switchBot == nil {
				return nil, fmt.Errorf("printer power control is not configured")
			}
			body = &switchBotPowerControl{client: p.switchBot, config: *cfg.PowerControl}
		default:
			return nil, fmt.Errorf("unsupported printer body power driver")
		}
	}
	return &printerController{id: id, settings: printerPowerSettings{cfg.autoStartDuration, cfg.startupDuration, cfg.shutdownDuration, cfg.pollDuration}, supplyReference: cfg.Supply, supply: supply, body: body, probe: &ippDeviceProbe{client: client, uri: cfg.IPPURI}, now: time.Now, sleep: p.sleep}, nil
}
