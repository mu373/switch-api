package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const powerUnknown = "unknown"
const supplyCleanupTimeout = 10 * time.Second

type supplyObservation struct {
	Output     bool
	PowerWatts *float64
}

type powerSupply interface {
	setSupply(context.Context, bool) error
	readSupply(context.Context) (supplyObservation, error)
}

type devicePowerControl interface {
	turnOn(context.Context) error
	turnOff(context.Context) error
}

type deviceStateProbe interface {
	readDeviceState(context.Context) (string, error)
}

type printerObservation struct {
	Supply      supplyObservation
	SupplyState string
	DeviceState string
}

// A missing meter or lost IPP connection is not proof that a powered body is OFF.
func classifyDevicePower(supply supplyObservation, probedState string) string {
	if probedState == "on" || probedState == "off" {
		return probedState
	}
	if !supply.Output || supply.PowerWatts != nil && *supply.PowerWatts == 0 {
		return "off"
	}
	return powerUnknown
}

type printerController struct {
	id              string
	settings        printerPowerSettings
	supplyReference deviceReference
	supply          powerSupply
	body            devicePowerControl
	probe           deviceStateProbe
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
}

type printerPowerSettings struct {
	autoStartWait, startupTimeout, shutdownWait, pollInterval time.Duration
}

func (c *printerController) readState(ctx context.Context) (printerObservation, error) {
	observation := printerObservation{SupplyState: powerUnknown, DeviceState: powerUnknown}
	supply, supplyErr := c.supply.readSupply(ctx)
	if supplyErr == nil {
		observation.Supply = supply
		observation.SupplyState = "off"
		if supply.Output {
			observation.SupplyState = "on"
		}
	}
	probedState, probeErr := c.probe.readDeviceState(ctx)
	if probeErr == nil && probedState != "on" && probedState != "off" && probedState != powerUnknown {
		probeErr = fmt.Errorf("device probe returned an invalid power state")
	}
	if probeErr == nil {
		if supplyErr == nil {
			observation.DeviceState = classifyDevicePower(supply, probedState)
		} else {
			observation.DeviceState = probedState
		}
	}
	return observation, errors.Join(supplyErr, probeErr)
}

func (c *printerController) readStatus(ctx context.Context) (switchStatus, error) {
	state, err := c.readState(ctx)
	result := switchStatus{Status: state.SupplyState, SupplyState: state.SupplyState, DeviceState: state.DeviceState, Devices: []switchDeviceStatus{}}
	if state.SupplyState != powerUnknown {
		result.Devices = append(result.Devices, switchDeviceStatus{Driver: c.supplyReference.Driver, DeviceID: c.supplyReference.DeviceID, Output: state.Supply.Output, PowerWatts: state.Supply.PowerWatts})
	}
	return result, err
}

func (c *printerController) setPower(ctx context.Context, requested switchState) (actionResult, error) {
	result := actionResult{Status: "completed", RequestedState: requested, Steps: []stepResult{}}
	operationContext := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		operationContext, cancel = context.WithDeadline(ctx, deadline.Add(-supplyCleanupTimeout))
		defer cancel()
	}
	var err error
	switch requested {
	case switchStateOn:
		err = c.turnOn(operationContext, &result)
		if err != nil {
			err = errors.Join(err, c.cutSupply(ctx, &result))
		}
	case switchStateOff:
		err = c.turnOff(operationContext, &result)
	default:
		err = fmt.Errorf("invalid requested printer power state")
	}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	}
	return result, err
}

func (c *printerController) recordOperation(result *actionResult, action string, err error) {
	step := stepResult{Index: len(result.Steps), Driver: "ipp-printer", DeviceID: c.id, Action: action, Status: "completed"}
	if err != nil {
		step.Status = "failed"
		step.Message = err.Error()
	}
	result.Steps = append(result.Steps, step)
}

func (c *printerController) turnOn(parent context.Context, result *actionResult) error {
	ctx, cancel := context.WithTimeout(parent, c.settings.startupTimeout)
	defer cancel()
	deadline := c.now().Add(c.settings.startupTimeout)
	state, err := c.readState(ctx)
	c.recordOperation(result, "check-state", err)
	if err != nil {
		return err
	}
	if state.SupplyState == "off" {
		err = c.supply.setSupply(ctx, true)
		c.recordOperation(result, "supply-on", err)
		if err != nil {
			return err
		}
		state, err = c.waitForOn(ctx, c.now().Add(c.settings.autoStartWait))
		if err != nil {
			c.recordOperation(result, "wait-auto-start", err)
			return err
		}
	}
	if state.DeviceState == "on" {
		result.SupplyState, result.DeviceState = "on", "on"
		c.recordOperation(result, "confirm-on", nil)
		return nil
	}
	if state.DeviceState == "off" && c.body != nil {
		if err := c.sleep(ctx, c.settings.pollInterval); err != nil {
			return err
		}
		state, err = c.readState(ctx)
		if err != nil {
			return err
		}
		if state.DeviceState == "off" {
			err = c.body.turnOn(ctx)
			c.recordOperation(result, "body-on", err)
			if err != nil {
				return err
			}
		}
	}
	if state.DeviceState != "on" {
		state, err = c.waitForOn(ctx, deadline)
	}
	if err == nil && state.DeviceState != "on" {
		err = fmt.Errorf("printer did not respond before startup timeout; refusing an unconfirmed ON button action")
	}
	c.recordOperation(result, "confirm-on", err)
	if err == nil {
		result.SupplyState, result.DeviceState = state.SupplyState, state.DeviceState
	}
	return err
}

func (c *printerController) waitForOn(ctx context.Context, deadline time.Time) (printerObservation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return printerObservation{}, err
		}
		state, err := c.readState(ctx)
		if err != nil || state.DeviceState == "on" || !c.now().Before(deadline) {
			return state, err
		}
		if err := c.sleep(ctx, min(c.settings.pollInterval, deadline.Sub(c.now()))); err != nil {
			return state, err
		}
	}
}

func (c *printerController) turnOff(ctx context.Context, result *actionResult) error {
	state, operationErr := c.readState(ctx)
	c.recordOperation(result, "check-state", operationErr)
	if operationErr == nil && state.DeviceState == "on" && c.body != nil {
		err := c.sleep(ctx, c.settings.pollInterval)
		if err == nil {
			state, err = c.readState(ctx)
		}
		if err == nil && state.DeviceState == "on" {
			err = c.body.turnOff(ctx)
			c.recordOperation(result, "body-off", err)
		}
		operationErr = errors.Join(operationErr, err)
	}
	if state.DeviceState != "off" || operationErr != nil {
		err := c.waitForShutdown(ctx)
		c.recordOperation(result, "wait-shutdown", err)
		operationErr = errors.Join(operationErr, err)
	}
	return errors.Join(operationErr, c.cutSupply(ctx, result))
}

// The grace period is bounded. Supply cutoff is the final OFF mechanism even if
// body shutdown cannot be confirmed (e.g. a plug-only printer or a network outage).
func (c *printerController) waitForShutdown(ctx context.Context) error {
	deadline := c.now().Add(c.settings.shutdownWait)
	consecutiveOff := 0
	for c.now().Before(deadline) {
		if err := c.sleep(ctx, min(c.settings.pollInterval, deadline.Sub(c.now()))); err != nil {
			return err
		}
		state, err := c.readState(ctx)
		if err != nil {
			return err
		}
		if state.DeviceState == "off" {
			consecutiveOff++
		} else {
			consecutiveOff = 0
		}
		if consecutiveOff >= 2 {
			return nil
		}
	}
	return nil
}

func (c *printerController) cutSupply(parent context.Context, result *actionResult) error {
	// Cleanup remains scoped to this printer even when the caller disconnects.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), supplyCleanupTimeout)
	defer cancel()
	err := c.supply.setSupply(ctx, false)
	c.recordOperation(result, "supply-off", err)
	if err != nil {
		result.SupplyState, result.DeviceState = powerUnknown, powerUnknown
		return err
	}
	state, err := c.readState(ctx)
	result.SupplyState, result.DeviceState = state.SupplyState, state.DeviceState
	if err == nil && (state.SupplyState != "off" || state.DeviceState != "off") {
		err = fmt.Errorf("printer power remained unconfirmed after supply cutoff")
	}
	c.recordOperation(result, "confirm-off", err)
	return err
}
