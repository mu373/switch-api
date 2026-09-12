package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSwitchBotCommand(t *testing.T) {
	const (
		token     = "test-token"
		secret    = "test-secret"
		timestamp = "1788998400123"
		nonce     = "00000000-0000-4000-8000-000000000001"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/v1.1/devices/BOT123/commands"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		for header, want := range map[string]string{
			"Authorization": token,
			"t":             timestamp,
			"nonce":         nonce,
			"sign":          signSwitchBot(token, secret, timestamp, nonce),
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got, want := body["command"], "turnOff"; got != want {
			t.Errorf("command = %q, want %q", got, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": 100, "message": "success", "body": map[string]any{}})
	}))
	defer server.Close()

	fixedTime, err := time.Parse(time.RFC3339Nano, "2026-09-10T00:00:00.123Z")
	if err != nil {
		t.Fatal(err)
	}
	client := &switchBotClient{
		baseURL: server.URL + "/v1.1",
		token:   token,
		secret:  secret,
		client:  server.Client(),
		now:     func() time.Time { return fixedTime },
		nonce:   func() (string, error) { return nonce, nil },
	}
	if err := client.command(context.Background(), "BOT123", "turnOff"); err != nil {
		t.Fatalf("command() error = %v", err)
	}
}

func TestSwitchBotRejectsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": 161, "message": "device offline"})
	}))
	defer server.Close()
	client := &switchBotClient{
		baseURL: server.URL,
		token:   "token",
		secret:  "secret",
		client:  server.Client(),
		now:     time.Now,
		nonce:   func() (string, error) { return "nonce", nil },
	}
	if err := client.command(context.Background(), "BOT123", "turnOn"); err == nil {
		t.Fatal("command() error = nil, want provider error")
	}
}

func TestShellySet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/rpc"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		var body struct {
			Method string `json:"method"`
			Params struct {
				ID int  `json:"id"`
				On bool `json:"on"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Method != "Switch.Set" || body.Params.ID != 2 || !body.Params.On {
			t.Errorf("unexpected Shelly request: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "result": map[string]bool{"was_on": false}})
	}))
	defer server.Close()
	client := &shellyClient{baseURL: server.URL, componentID: 2, client: server.Client()}
	if err := client.set(context.Background(), true); err != nil {
		t.Fatalf("set() error = %v", err)
	}
}

func TestOptionalShellyStepIsSkipped(t *testing.T) {
	providerSet := &providers{shelly: map[string]*shellyClient{}, sleep: sleepContext}
	result, err := providerSet.run(context.Background(), stepConfig{
		Driver: "shelly-gen2", DeviceID: "missing", Action: "on", Optional: true,
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got, want := result.Status, "skipped"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestShellyVerification(t *testing.T) {
	for _, test := range []struct {
		name       string
		action     string
		response   string
		wantError  string
		wantOutput *bool
		wantPower  *float64
	}{
		{name: "on", action: "verify-on", response: `{"id":1,"result":{"id":2,"output":true,"apower":12.5}}`, wantOutput: testPointer(true), wantPower: testPointer(12.5)},
		{name: "off with zero power", action: "verify-off", response: `{"id":1,"result":{"id":2,"output":false,"apower":0}}`, wantOutput: testPointer(false), wantPower: testPointer(0.0)},
		{name: "no power meter", action: "verify-on", response: `{"id":1,"result":{"id":2,"output":true}}`, wantOutput: testPointer(true)},
		{name: "on mismatch", action: "verify-on", response: `{"id":1,"result":{"id":2,"output":false}}`, wantError: "expected true", wantOutput: testPointer(false)},
		{name: "off mismatch", action: "verify-off", response: `{"id":1,"result":{"id":2,"output":true}}`, wantError: "expected false", wantOutput: testPointer(true)},
		{name: "device fault", action: "verify-on", response: `{"id":1,"result":{"id":2,"output":true,"errors":["overtemp"]}}`, wantError: "overtemp", wantOutput: testPointer(true)},
		{name: "missing output", action: "verify-off", response: `{"id":1,"result":{"id":2}}`, wantError: "no output"},
		{name: "null output", action: "verify-off", response: `{"id":1,"result":{"id":2,"output":null}}`, wantError: "no output"},
		{name: "missing component", action: "verify-off", response: `{"id":1,"result":{"output":false}}`, wantError: "component ID", wantOutput: testPointer(false)},
		{name: "wrong component", action: "verify-off", response: `{"id":1,"result":{"id":0,"output":false}}`, wantError: "component ID", wantOutput: testPointer(false)},
		{name: "wrong RPC ID", action: "verify-off", response: `{"id":3,"result":{"id":2,"output":false}}`, wantError: "RPC ID"},
		{name: "missing result", action: "verify-off", response: `{"id":1}`, wantError: "no result"},
		{name: "null result", action: "verify-off", response: `{"id":1,"result":null}`, wantError: "no result"},
		{name: "empty response", action: "verify-off", response: `{}`, wantError: "RPC ID"},
		{name: "invalid JSON", action: "verify-off", response: `not JSON`, wantError: "decode Shelly response"},
		{name: "provider error", action: "verify-off", response: `{"id":1,"error":{"code":-105,"message":"unavailable"}}`, wantError: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string `json:"method"`
					Params struct {
						ID int `json:"id"`
					} `json:"params"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Method != "Switch.GetStatus" || request.Params.ID != 2 {
					t.Errorf("unexpected RPC: %+v", request)
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			providerSet := &providers{sleep: func(context.Context, time.Duration) error { return nil }, shelly: map[string]*shellyClient{
				"plug": {baseURL: server.URL, componentID: 2, client: server.Client()},
			}}
			result, err := providerSet.run(context.Background(), stepConfig{Driver: "shelly-gen2", DeviceID: "plug", Action: test.action})
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("run() error = %v, want %q", err, test.wantError)
			}
			if (result.Output == nil) != (test.wantOutput == nil) || result.Output != nil && *result.Output != *test.wantOutput {
				t.Fatalf("output = %v, want %v", result.Output, test.wantOutput)
			}
			if (result.PowerWatts == nil) != (test.wantPower == nil) || result.PowerWatts != nil && *result.PowerWatts != *test.wantPower {
				t.Fatalf("power = %v, want %v", result.PowerWatts, test.wantPower)
			}
		})
	}
}

func testPointer[T any](value T) *T { return &value }

func TestShellyVerificationRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"id":1,"result":{"id":0,"output":false}}`))
	}))
	defer server.Close()
	client := &shellyClient{baseURL: server.URL, client: server.Client()}
	if _, err := client.verifyOutput(context.Background(), false); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("verifyOutput() error = %v, want HTTP failure", err)
	}
}

func TestShellySetRejectsInvalidAcknowledgement(t *testing.T) {
	for _, response := range []string{`{}`, `{"id":1}`, `{"id":1,"result":{}}`, `{"id":1,"result":null}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			client := &shellyClient{baseURL: server.URL, client: server.Client()}
			if err := client.set(context.Background(), true); err == nil {
				t.Fatal("set() accepted an invalid acknowledgement")
			}
		})
	}
}

func TestShellyVerificationRetriesUntilOutputMatches(t *testing.T) {
	for _, test := range []struct {
		name        string
		succeedsOn  int
		wantCalls   int
		wantFailure bool
	}{
		{name: "first attempt", succeedsOn: 1, wantCalls: 1},
		{name: "second attempt", succeedsOn: 2, wantCalls: 2},
		{name: "third attempt", succeedsOn: 3, wantCalls: 3},
		{name: "exhausted", succeedsOn: 4, wantCalls: 3, wantFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, waits := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Method != "Switch.GetStatus" {
					t.Errorf("retry changed output: %s", request.Method)
				}
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "result": map[string]any{"id": 0, "output": calls >= test.succeedsOn}})
			}))
			defer server.Close()
			providerSet := &providers{
				shelly: map[string]*shellyClient{"plug": {baseURL: server.URL, client: server.Client()}},
				sleep: func(_ context.Context, interval time.Duration) error {
					waits++
					if interval != time.Second {
						t.Errorf("retry interval = %v, want 1s", interval)
					}
					return nil
				},
			}
			result, err := providerSet.run(context.Background(), stepConfig{Driver: "shelly-gen2", DeviceID: "plug", Action: "verify-on"})
			if (err != nil) != test.wantFailure || calls != test.wantCalls || waits != test.wantCalls-1 {
				t.Fatalf("error = %v, calls = %d, waits = %d", err, calls, waits)
			}
			if test.wantFailure && !strings.Contains(err.Error(), "after 3 attempts") {
				t.Fatalf("error = %v", err)
			}
			if result.Output == nil || *result.Output == test.wantFailure {
				t.Fatalf("unexpected final output: %+v", result)
			}
		})
	}
}

func TestShellyVerificationStopsWhenRetryIsCancelled(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"id":1,"result":{"id":0,"output":false}}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providerSet := &providers{
		shelly: map[string]*shellyClient{"plug": {baseURL: server.URL, client: server.Client()}},
		sleep:  func(waitCtx context.Context, _ time.Duration) error { cancel(); return waitCtx.Err() },
	}
	_, err := providerSet.run(ctx, stepConfig{Driver: "shelly-gen2", DeviceID: "plug", Action: "verify-on"})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}
