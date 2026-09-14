package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestClassifyDevicePower(t *testing.T) {
	for _, test := range []struct {
		name        string
		supply      supplyObservation
		probe, want string
	}{
		{"sleep at zero watts", supplyObservation{true, testPointer(0.0)}, "on", "on"},
		{"zero and disconnected", supplyObservation{true, testPointer(0.0)}, powerUnknown, "off"},
		{"starting or network fault", supplyObservation{true, testPointer(3.0)}, powerUnknown, powerUnknown},
		{"missing meter", supplyObservation{true, nil}, powerUnknown, powerUnknown},
		{"missing meter but responsive", supplyObservation{true, nil}, "on", "on"},
		{"supply off", supplyObservation{false, nil}, powerUnknown, "off"},
		{"future API confirms body off despite standby draw", supplyObservation{true, testPointer(1.0)}, "off", "off"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyDevicePower(test.supply, test.probe); got != test.want {
				t.Fatalf("got=%s want=%s", got, test.want)
			}
		})
	}
}

type fakePowerSupply struct {
	observation         supplyObservation
	calls               []bool
	readError, cutError error
	onSet               func(bool)
}

func (s *fakePowerSupply) readSupply(context.Context) (supplyObservation, error) {
	return s.observation, s.readError
}
func (s *fakePowerSupply) setSupply(ctx context.Context, output bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls = append(s.calls, output)
	if !output && s.cutError != nil {
		return s.cutError
	}
	s.observation.Output = output
	if s.onSet != nil {
		s.onSet(output)
	}
	return nil
}

type fakeStateProbe struct {
	state string
	err   error
	reads []string
}

func (p *fakeStateProbe) readDeviceState(context.Context) (string, error) {
	state := p.state
	if len(p.reads) > 0 {
		state = p.reads[0]
		p.reads = p.reads[1:]
	}
	return state, p.err
}

type fakeBodyPower struct {
	calls     []string
	err       error
	onCommand func(string)
}

func (p *fakeBodyPower) turnOn(context.Context) error  { return p.send("on") }
func (p *fakeBodyPower) turnOff(context.Context) error { return p.send("off") }
func (p *fakeBodyPower) send(state string) error {
	p.calls = append(p.calls, state)
	if p.onCommand != nil {
		p.onCommand(state)
	}
	return p.err
}

func createTestPrinter() (*printerController, *fakePowerSupply, *fakeBodyPower, *fakeStateProbe) {
	probe := &fakeStateProbe{state: powerUnknown}
	supply := &fakePowerSupply{observation: supplyObservation{PowerWatts: testPointer(0.0)}}
	supply.onSet = func(output bool) {
		if !output {
			probe.state = powerUnknown
		}
	}
	body := &fakeBodyPower{onCommand: func(state string) {
		if state == "on" {
			probe.state = "on"
		} else {
			probe.state = powerUnknown
		}
	}}
	now := time.Unix(0, 0)
	controller := &printerController{
		id: "test-printer", settings: printerPowerSettings{2 * time.Second, 5 * time.Second, 3 * time.Second, time.Second}, supplyReference: deviceReference{"test-supply", "plug"},
		supply: supply, body: body, probe: probe, now: func() time.Time { return now }, sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			now = now.Add(d)
			return nil
		},
	}
	return controller, supply, body, probe
}

func TestPrinterAutoStartDoesNotPressBodyButton(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.PowerWatts = nil // A meter is not required when the printer responds.
	supply.onSet = func(output bool) {
		if output {
			probe.state = "on"
		} else {
			probe.state = powerUnknown
		}
	}
	result, err := c.setPower(context.Background(), switchStateOn)
	if err != nil || result.DeviceState != "on" || !reflect.DeepEqual(supply.calls, []bool{true}) || len(body.calls) != 0 {
		t.Fatalf("result=%+v err=%v supply=%v body=%v", result, err, supply.calls, body.calls)
	}
}

func TestPrinterWakesConfirmedStoppedBodyOnce(t *testing.T) {
	c, supply, body, _ := createTestPrinter()
	result, err := c.setPower(context.Background(), switchStateOn)
	if err != nil || result.DeviceState != "on" || !reflect.DeepEqual(body.calls, []string{"on"}) || !reflect.DeepEqual(supply.calls, []bool{true}) {
		t.Fatalf("result=%+v err=%v body=%v supply=%v", result, err, body.calls, supply.calls)
	}
}

func TestPrinterRepeatedONSkipsPowerCommandsAtZeroWatts(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.Output = true
	probe.state = "on"
	for i := 0; i < 2; i++ {
		if _, err := c.setPower(context.Background(), switchStateOn); err != nil {
			t.Fatal(err)
		}
	}
	if len(supply.calls) != 0 || len(body.calls) != 0 {
		t.Fatalf("supply=%v body=%v", supply.calls, body.calls)
	}
}

func TestPrinterStartsBetweenChecksWithoutButtonPress(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.Output = true
	probe.reads = []string{powerUnknown, "on"}
	if _, err := c.setPower(context.Background(), switchStateOn); err != nil {
		t.Fatal(err)
	}
	if len(body.calls) != 0 {
		t.Fatalf("unexpected body command %v", body.calls)
	}
}

func TestPrinterUnknownStateNeverTriggersONButton(t *testing.T) {
	for _, watts := range []*float64{nil, testPointer(3.0)} {
		c, supply, body, _ := createTestPrinter()
		supply.observation = supplyObservation{true, watts}
		result, err := c.setPower(context.Background(), switchStateOn)
		if err == nil || result.Status != "failed" || len(body.calls) != 0 || !reflect.DeepEqual(supply.calls, []bool{false}) {
			t.Fatalf("result=%+v err=%v body=%v supply=%v", result, err, body.calls, supply.calls)
		}
	}
}

func TestPlugOnlyPrinterCanAutoStartAndCutSupply(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	c.body = nil
	supply.onSet = func(output bool) {
		if output {
			probe.state = "on"
		} else {
			probe.state = powerUnknown
		}
	}
	for _, state := range []switchState{switchStateOn, switchStateOff} {
		if _, err := c.setPower(context.Background(), state); err != nil {
			t.Fatal(err)
		}
	}
	if len(body.calls) != 0 || !reflect.DeepEqual(supply.calls, []bool{true, false}) {
		t.Fatalf("body=%v supply=%v", body.calls, supply.calls)
	}
}

func TestPrinterSleepingBodyReceivesOFFBeforeSupplyCut(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.Output = true
	probe.state = "on"
	result, err := c.setPower(context.Background(), switchStateOff)
	if err != nil || result.SupplyState != "off" || result.DeviceState != "off" || !reflect.DeepEqual(body.calls, []string{"off"}) || !reflect.DeepEqual(supply.calls, []bool{false}) {
		t.Fatalf("result=%+v err=%v body=%v supply=%v", result, err, body.calls, supply.calls)
	}
}

func TestPrinterStoppedBodyNeverReceivesOFFButton(t *testing.T) {
	for _, output := range []bool{true, false} {
		c, supply, body, _ := createTestPrinter()
		supply.observation.Output = output
		if _, err := c.setPower(context.Background(), switchStateOff); err != nil {
			t.Fatal(err)
		}
		if len(body.calls) != 0 || !reflect.DeepEqual(supply.calls, []bool{false}) {
			t.Fatalf("body=%v supply=%v", body.calls, supply.calls)
		}
	}
}

func TestPrinterOFFPreservesButtonErrorAfterSupplyCleanup(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.Output = true
	probe.state = "on"
	body.err = errors.New("body action failed")
	result, err := c.setPower(context.Background(), switchStateOff)
	if !errors.Is(err, body.err) || result.Status != "failed" || result.SupplyState != "off" || !reflect.DeepEqual(supply.calls, []bool{false}) {
		t.Fatalf("result=%+v err=%v supply=%v", result, err, supply.calls)
	}
}

func TestPrinterCleanupSurvivesCallerCancellation(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	supply.observation.Output = true
	probe.state = "on"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := c.setPower(ctx, switchStateOff)
	if !errors.Is(err, context.Canceled) || result.SupplyState != "off" || len(body.calls) != 0 || !reflect.DeepEqual(supply.calls, []bool{false}) {
		t.Fatalf("result=%+v err=%v body=%v supply=%v", result, err, body.calls, supply.calls)
	}
}

func TestPrinterCleanupPreservesProbeAndSupplyErrors(t *testing.T) {
	c, supply, body, probe := createTestPrinter()
	probe.err = errors.New("invalid probe response")
	supply.cutError = errors.New("supply cutoff failed")
	result, err := c.setPower(context.Background(), switchStateOff)
	if !errors.Is(err, probe.err) || !errors.Is(err, supply.cutError) || result.SupplyState != powerUnknown || len(body.calls) != 0 || !reflect.DeepEqual(supply.calls, []bool{false}) {
		t.Fatalf("result=%+v err=%v supply=%v", result, err, supply.calls)
	}
}

func TestPrinterStatusSeparatesSupplyAndBodyState(t *testing.T) {
	c, supply, _, _ := createTestPrinter()
	supply.observation = supplyObservation{true, nil}
	status, err := c.readStatus(context.Background())
	if err != nil || status.Status != "on" || status.SupplyState != "on" || status.DeviceState != powerUnknown {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
