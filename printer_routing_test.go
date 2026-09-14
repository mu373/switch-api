package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type blockingSwitchController struct {
	entered chan struct{}
	release chan struct{}
}

func (c *blockingSwitchController) setPower(ctx context.Context, state switchState) (actionResult, error) {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return actionResult{Status: "completed", SupplyState: string(state), DeviceState: string(state)}, nil
	case <-ctx.Done():
		return actionResult{}, ctx.Err()
	}
}
func (c *blockingSwitchController) readStatus(context.Context) (switchStatus, error) {
	return switchStatus{Status: "on", SupplyState: "on", DeviceState: powerUnknown, Devices: []switchDeviceStatus{}}, nil
}

func TestPrinterAliasesSerializeWhileDifferentPrintersRunIndependently(t *testing.T) {
	first := &blockingSwitchController{make(chan struct{}, 2), make(chan struct{})}
	second := &blockingSwitchController{make(chan struct{}, 1), make(chan struct{})}
	defer close(first.release)
	defer close(second.release)
	p := &providers{controllers: map[deviceReference]switchController{{"ipp-printer", "first"}: first, {"ipp-printer", "second"}: second}}
	cfg := config{actionDuration: time.Second, Switches: []logicalSwitchConfig{{ID: "first", Driver: "ipp-printer", DeviceID: "first"}, {ID: "alias", Driver: "ipp-printer", DeviceID: "first"}, {ID: "second", Driver: "ipp-printer", DeviceID: "second"}}}
	s := newSwitchService(cfg, p)
	if s.locks["first"] != s.locks["alias"] || s.locks["first"] == s.locks["second"] {
		t.Fatal("incorrect device lock ownership")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.execute(ctx, "first", switchStateOn)
	select {
	case <-first.entered:
	case <-time.After(time.Second):
		t.Fatal("first did not enter")
	}
	go s.execute(ctx, "alias", switchStateOff)
	go s.execute(ctx, "second", switchStateOn)
	select {
	case <-second.entered:
	case <-time.After(time.Second):
		t.Fatal("independent printer was blocked")
	}
	select {
	case <-first.entered:
		t.Fatal("alias bypassed device lock")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPrinterHTTPStatusAndActionsKeepExistingRoutes(t *testing.T) {
	c, _, _, _ := createTestPrinter()
	p := &providers{controllers: map[deviceReference]switchController{{"ipp-printer", "printer"}: c}}
	s := newSwitchService(config{actionDuration: 90 * time.Second, Switches: []logicalSwitchConfig{{ID: "printer", Driver: "ipp-printer", DeviceID: "printer"}}}, p)
	h := newHTTPHandler(s, "test-key")
	for _, test := range []struct{ method, path string }{{http.MethodPost, "/switches/printer/on"}, {http.MethodGet, "/switches/printer/status"}, {http.MethodPost, "/switches/printer/off"}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set("X-Api-Key", "test-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", test.method, test.path, w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["switch_id"] != "printer" || result["supply_state"] == nil || result["device_state"] == nil {
			t.Fatalf("incomplete response %v", result)
		}
	}
}
