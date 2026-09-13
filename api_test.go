package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAPI(t *testing.T) {
	cfg := serviceTestConfig()
	service := newSwitchService(cfg, stepRunnerFunc(func(_ context.Context, step stepConfig) (stepResult, error) {
		return stepResult{Driver: step.Driver, Action: step.Action, Status: "completed"}, nil
	}))
	server := httptest.NewServer(newHTTPHandler(service, "secret"))
	defer server.Close()

	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got, want := response.StatusCode, http.StatusOK; got != want {
		t.Fatalf("health status = %d, want %d", got, want)
	}

	response, err = http.Get(server.URL + "/switches")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got, want := response.StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("unauthorized status = %d, want %d", got, want)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/switches/printer/on", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got, want := response.StatusCode, http.StatusOK; got != want {
		t.Fatalf("switch on status = %d, want %d", got, want)
	}
}

func TestHTTPAPINotFound(t *testing.T) {
	service := newSwitchService(serviceTestConfig(), stepRunnerFunc(func(context.Context, stepConfig) (stepResult, error) {
		return stepResult{}, nil
	}))
	server := httptest.NewServer(newHTTPHandler(service, "secret"))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/switches/missing/off", nil)
	request.Header.Set("X-Api-Key", "secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got, want := response.StatusCode, http.StatusNotFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPReadsRelayStateWithoutRunningCommands(t *testing.T) {
	for _, output := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[output], func(t *testing.T) {
			calls := 0
			device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Method != "Switch.GetStatus" {
					t.Errorf("status request mutated relay: %s", request.Method)
				}
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "result": map[string]any{"id": 0, "output": output, "apower": 0}})
			}))
			defer device.Close()
			cfg := serviceTestConfig()
			cfg.Switches[0].On = []stepConfig{{Driver: "shelly-gen2", DeviceID: "plug", Action: "on"}, {Driver: "shelly-gen2", DeviceID: "plug", Action: "verify-on"}}
			cfg.Switches[0].Off = []stepConfig{{Driver: "shelly-gen2", DeviceID: "plug", Action: "off"}}
			providerSet := &providers{shelly: map[string]*shellyClient{"plug": {baseURL: device.URL, client: device.Client()}}}
			server := httptest.NewServer(newHTTPHandler(newSwitchService(cfg, providerSet), "test-key"))
			defer server.Close()
			for _, test := range []struct {
				path, key string
				status    int
			}{
				{path: "/switches/printer/status", status: http.StatusUnauthorized},
				{path: "/switches/missing/status", key: "test-key", status: http.StatusNotFound},
				{path: "/switches/printer/status", key: "test-key", status: http.StatusOK},
			} {
				req, err := http.NewRequest(http.MethodGet, server.URL+test.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-Api-Key", test.key)
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != test.status {
					t.Errorf("status = %d, want %d", resp.StatusCode, test.status)
				}
				if test.status == http.StatusOK {
					var result switchStatus
					if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
						t.Fatal(err)
					}
					if result.Status != map[bool]string{false: "off", true: "on"}[output] || len(result.Devices) != 1 || result.Devices[0].Output != output || result.Devices[0].PowerWatts == nil || *result.Devices[0].PowerWatts != 0 {
						t.Fatalf("unexpected status: %+v", result)
					}
				}
				resp.Body.Close()
			}
			if calls != 1 {
				t.Errorf("device reads = %d, want one authenticated unique-device read", calls)
			}
		})
	}
}
