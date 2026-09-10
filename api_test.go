package main

import (
	"context"
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
