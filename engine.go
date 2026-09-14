package main

import (
	"context"
	"fmt"
)

type switchState string

const (
	switchStateOn  switchState = "on"
	switchStateOff switchState = "off"
)

type stepResult struct {
	Index      int      `json:"index"`
	Driver     string   `json:"driver"`
	DeviceID   string   `json:"device_id,omitempty"`
	Action     string   `json:"action,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	Status     string   `json:"status"`
	Message    string   `json:"message,omitempty"`
	Output     *bool    `json:"output,omitempty"`
	PowerWatts *float64 `json:"power_watts,omitempty"`
}

type actionResult struct {
	SwitchID       string       `json:"switch_id"`
	RequestedState switchState  `json:"requested_state"`
	Status         string       `json:"status"`
	Steps          []stepResult `json:"steps"`
	Error          string       `json:"error,omitempty"`
	SupplyState    string       `json:"supply_state,omitempty"`
	DeviceState    string       `json:"device_state,omitempty"`
}

type stepRunner interface {
	run(context.Context, stepConfig) (stepResult, error)
}

type switchStatus struct {
	SwitchID    string               `json:"switch_id"`
	Status      string               `json:"status"`
	Devices     []switchDeviceStatus `json:"devices"`
	SupplyState string               `json:"supply_state,omitempty"`
	DeviceState string               `json:"device_state,omitempty"`
}

type switchController interface {
	setPower(context.Context, switchState) (actionResult, error)
	readStatus(context.Context) (switchStatus, error)
}

type switchControllerProvider interface {
	getSwitchController(logicalSwitchConfig) (switchController, error)
}

type switchDeviceStatus struct {
	Driver     string   `json:"driver"`
	DeviceID   string   `json:"device_id"`
	Output     bool     `json:"output"`
	PowerWatts *float64 `json:"power_watts,omitempty"`
}

type switchStatusReader interface {
	readSwitchStatus(context.Context, logicalSwitchConfig) (switchStatus, error)
}

type switchService struct {
	switches map[string]logicalSwitchConfig
	order    []string
	locks    map[string]chan struct{}
	runner   stepRunner
	timeout  func(context.Context) (context.Context, context.CancelFunc)
}

func newSwitchService(cfg config, runner stepRunner) *switchService {
	switches := make(map[string]logicalSwitchConfig, len(cfg.Switches))
	locks := make(map[string]chan struct{}, len(cfg.Switches))
	order := make([]string, 0, len(cfg.Switches))
	deviceLocks := map[deviceReference]chan struct{}{}
	for _, configured := range cfg.Switches {
		switches[configured.ID] = configured
		locks[configured.ID] = make(chan struct{}, 1)
		if configured.Driver != "" {
			reference := deviceReference{configured.Driver, configured.DeviceID}
			if lock, ok := deviceLocks[reference]; ok {
				locks[configured.ID] = lock
			} else {
				deviceLocks[reference] = locks[configured.ID]
			}
		}
		order = append(order, configured.ID)
	}
	return &switchService{
		switches: switches,
		order:    order,
		locks:    locks,
		runner:   runner,
		timeout: func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(parent, cfg.actionDuration)
		},
	}
}

func (s *switchService) list() []logicalSwitchConfig {
	result := make([]logicalSwitchConfig, 0, len(s.order))
	for _, id := range s.order {
		result = append(result, s.switches[id])
	}
	return result
}

func (s *switchService) execute(parent context.Context, id string, state switchState) (actionResult, error) {
	configured, ok := s.switches[id]
	if !ok {
		return actionResult{}, errSwitchNotFound
	}
	ctx, cancel := s.timeout(parent)
	defer cancel()

	lock := s.locks[id]
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return actionResult{}, ctx.Err()
	}

	if configured.Driver != "" {
		provider, ok := s.runner.(switchControllerProvider)
		if !ok {
			return actionResult{}, fmt.Errorf("device controller is unavailable")
		}
		controller, err := provider.getSwitchController(configured)
		if err != nil {
			return actionResult{}, err
		}
		result, err := controller.setPower(ctx, state)
		result.SwitchID, result.RequestedState = id, state
		return result, err
	}
	steps := configured.On
	if state == switchStateOff {
		steps = configured.Off
	}
	result := actionResult{
		SwitchID:       id,
		RequestedState: state,
		Status:         "completed",
		Steps:          make([]stepResult, 0, len(steps)),
	}
	for index, step := range steps {
		stepResult, err := s.runner.run(ctx, step)
		stepResult.Index = index
		if err != nil {
			stepResult.Status = "failed"
			stepResult.Message = err.Error()
			result.Steps = append(result.Steps, stepResult)
			result.Status = "failed"
			result.Error = fmt.Sprintf("step %d failed: %v", index, err)
			return result, err
		}
		result.Steps = append(result.Steps, stepResult)
	}
	return result, nil
}

var errSwitchNotFound = fmt.Errorf("switch not found")

func (s *switchService) readStatus(parent context.Context, id string) (switchStatus, error) {
	configured, ok := s.switches[id]
	if !ok {
		return switchStatus{}, errSwitchNotFound
	}
	ctx, cancel := s.timeout(parent)
	defer cancel()
	if configured.Driver != "" {
		provider, ok := s.runner.(switchControllerProvider)
		if !ok {
			return switchStatus{}, fmt.Errorf("device controller is unavailable")
		}
		controller, err := provider.getSwitchController(configured)
		if err != nil {
			return switchStatus{}, err
		}
		result, err := controller.readStatus(ctx)
		result.SwitchID = id
		return result, err
	}
	reader, ok := s.runner.(switchStatusReader)
	if !ok {
		return switchStatus{}, fmt.Errorf("switch status is unavailable")
	}
	return reader.readSwitchStatus(ctx, configured)
}
