package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
