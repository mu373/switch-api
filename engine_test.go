package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type stepRunnerFunc func(context.Context, stepConfig) (stepResult, error)

func (f stepRunnerFunc) run(ctx context.Context, step stepConfig) (stepResult, error) {
	return f(ctx, step)
}

func TestSwitchServiceExecutesStepsInOrder(t *testing.T) {
	cfg := serviceTestConfig()
	var actions []string
	service := newSwitchService(cfg, stepRunnerFunc(func(_ context.Context, step stepConfig) (stepResult, error) {
		actions = append(actions, step.Action)
		return stepResult{Driver: step.Driver, Action: step.Action, Status: "completed"}, nil
	}))
	result, err := service.execute(context.Background(), "printer", switchStateOn)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if got, want := actions, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	if result.Status != "completed" || len(result.Steps) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSwitchServiceStopsAfterFailure(t *testing.T) {
	cfg := serviceTestConfig()
	calls := 0
	service := newSwitchService(cfg, stepRunnerFunc(func(_ context.Context, step stepConfig) (stepResult, error) {
		calls++
		if calls == 1 {
			return stepResult{Driver: step.Driver}, errors.New("failed")
		}
		return stepResult{}, nil
	}))
	result, err := service.execute(context.Background(), "printer", switchStateOn)
	if err == nil {
		t.Fatal("execute() error = nil, want failure")
	}
	if calls != 1 || result.Status != "failed" || result.Steps[0].Status != "failed" {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
}

func serviceTestConfig() config {
	return config{
		actionDuration: time.Second,
		Switches: []logicalSwitchConfig{{
			ID:          "printer",
			DisplayName: "Printer",
			On: []stepConfig{
				{Driver: "test", Action: "first"},
				{Driver: "test", Action: "second"},
			},
			Off: []stepConfig{{Driver: "test", Action: "off"}},
		}},
	}
}
