package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestSwitchServiceStopsBeforeButtonWhenShellyIsOff(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			t.Error("power button was called before relay output was confirmed")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		methods = append(methods, request.Method)
		if request.Method == "Switch.Set" {
			_, _ = w.Write([]byte(`{"id":1,"result":{"was_on":false}}`))
		} else {
			_, _ = w.Write([]byte(`{"id":1,"result":{"id":0,"output":false}}`))
		}
	}))
	defer server.Close()
	cfg := serviceTestConfig()
	cfg.Switches[0].On = []stepConfig{
		{Driver: "shelly-gen2", DeviceID: "plug", Action: "on"},
		{Driver: "shelly-gen2", DeviceID: "plug", Action: "verify-on"},
		{Driver: "switchbot", DeviceID: "bot", Action: "turnOn"},
	}
	providerSet := &providers{
		sleep:     func(context.Context, time.Duration) error { return nil },
		shelly:    map[string]*shellyClient{"plug": {baseURL: server.URL, client: server.Client()}},
		switchBot: &switchBotClient{baseURL: server.URL, client: server.Client(), now: time.Now, nonce: func() (string, error) { return "test", nil }},
	}
	result, err := newSwitchService(cfg, providerSet).execute(context.Background(), "printer", switchStateOn)
	if err == nil || result.Status != "failed" || len(result.Steps) != 2 || result.Steps[1].Status != "failed" {
		t.Fatalf("error = %v, result = %+v, want verification failure", err, result)
	}
	if !reflect.DeepEqual(methods, []string{"Switch.Set", "Switch.GetStatus", "Switch.GetStatus", "Switch.GetStatus"}) {
		t.Fatalf("RPC methods = %v", methods)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	step := decoded["steps"].([]any)[1].(map[string]any)
	if output, ok := step["output"].(bool); !ok || output {
		t.Fatalf("failed step does not retain observed OFF state: %s", encoded)
	}
}
