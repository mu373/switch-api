package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxProviderResponseBytes = 1 << 20
	shellyVerifyAttempts     = 3
	shellyVerifyInterval     = time.Second
)

type providers struct {
	switchBot *switchBotClient
	shelly    map[string]*shellyClient
	sleep     func(context.Context, time.Duration) error
}

func newProviders(cfg config) (*providers, error) {
	httpClient := &http.Client{Timeout: cfg.actionDuration}
	result := &providers{
		shelly: make(map[string]*shellyClient, len(cfg.ShellyDevices)),
		sleep:  sleepContext,
	}
	if cfg.SwitchBot != nil {
		token := os.Getenv(cfg.SwitchBot.TokenEnv)
		secret := os.Getenv(cfg.SwitchBot.SecretEnv)
		if token == "" {
			return nil, fmt.Errorf("switchbot token environment variable %s is empty", cfg.SwitchBot.TokenEnv)
		}
		if secret == "" {
			return nil, fmt.Errorf("switchbot secret environment variable %s is empty", cfg.SwitchBot.SecretEnv)
		}
		result.switchBot = &switchBotClient{
			baseURL: strings.TrimRight(cfg.SwitchBot.BaseURL, "/"),
			token:   token,
			secret:  secret,
			client:  httpClient,
			now:     time.Now,
			nonce:   newNonce,
		}
	}
	for id, device := range cfg.ShellyDevices {
		result.shelly[id] = &shellyClient{
			baseURL:     strings.TrimRight(device.BaseURL, "/"),
			componentID: device.ComponentID,
			client:      httpClient,
		}
	}
	return result, nil
}

func (p *providers) run(ctx context.Context, step stepConfig) (stepResult, error) {
	result := stepResult{
		Driver:   step.Driver,
		DeviceID: step.DeviceID,
		Action:   step.Action,
		Status:   "completed",
	}

	switch step.Driver {
	case "switchbot":
		if p.switchBot == nil {
			if step.Optional {
				result.Status = "skipped"
				result.Message = "SwitchBot provider is not configured"
				return result, nil
			}
			return result, fmt.Errorf("SwitchBot provider is not configured")
		}
		if err := p.switchBot.command(ctx, step.DeviceID, step.Action); err != nil {
			return result, err
		}
	case "shelly-gen2":
		device, ok := p.shelly[step.DeviceID]
		if !ok {
			if step.Optional {
				result.Status = "skipped"
				result.Message = "Shelly device is not configured"
				return result, nil
			}
			return result, fmt.Errorf("Shelly device %q is not configured", step.DeviceID)
		}
		switch step.Action {
		case "on", "off":
			if err := device.set(ctx, step.Action == "on"); err != nil {
				return result, err
			}
		case "verify-on", "verify-off":
			status, err := p.verifyShellyOutput(ctx, device, step.Action == "verify-on")
			result.Output = status.Output
			result.PowerWatts = status.PowerWatts
			if err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unsupported Shelly action %q", step.Action)
		}
	case "delay":
		duration, _ := time.ParseDuration(step.Duration)
		result.Duration = duration.String()
		if err := p.sleep(ctx, duration); err != nil {
			return result, err
		}
	default:
		return result, fmt.Errorf("unsupported driver %q", step.Driver)
	}
	return result, nil
}

type switchBotClient struct {
	baseURL string
	token   string
	secret  string
	client  *http.Client
	now     func() time.Time
	nonce   func() (string, error)
}

func (c *switchBotClient) command(ctx context.Context, deviceID, command string) error {
	body, err := json.Marshal(map[string]string{
		"command":     command,
		"parameter":   "default",
		"commandType": "command",
	})
	if err != nil {
		return fmt.Errorf("encode SwitchBot command: %w", err)
	}
	nonce, err := c.nonce()
	if err != nil {
		return fmt.Errorf("create SwitchBot nonce: %w", err)
	}
	timestamp := strconv.FormatInt(c.now().UnixMilli(), 10)
	signature := signSwitchBot(c.token, c.secret, timestamp, nonce)
	endpoint := c.baseURL + "/devices/" + url.PathEscape(deviceID) + "/commands"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create SwitchBot request: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("sign", signature)
	req.Header.Set("t", timestamp)
	req.Header.Set("nonce", nonce)
	req.Header.Set("Content-Type", "application/json; charset=utf8")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call SwitchBot: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes))
	if err != nil {
		return fmt.Errorf("read SwitchBot response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SwitchBot returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return fmt.Errorf("decode SwitchBot response: %w", err)
	}
	if result.StatusCode != 100 {
		return fmt.Errorf("SwitchBot rejected command with status %d: %s", result.StatusCode, result.Message)
	}
	return nil
}

func signSwitchBot(token, secret, timestamp, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(token + timestamp + nonce))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func newNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

type shellyClient struct {
	baseURL     string
	componentID int
	client      *http.Client
}

func (c *shellyClient) set(ctx context.Context, on bool) error {
	var result struct {
		WasOn *bool `json:"was_on"`
	}
	if err := c.callRPC(ctx, "Switch.Set", struct {
		ID int  `json:"id"`
		On bool `json:"on"`
	}{ID: c.componentID, On: on}, &result); err != nil {
		return err
	}
	if result.WasOn == nil {
		return fmt.Errorf("Shelly Switch.Set response has no was_on")
	}
	return nil
}

type shellySwitchStatus struct {
	ID         *int     `json:"id"`
	Output     *bool    `json:"output"`
	PowerWatts *float64 `json:"apower"`
	Errors     []string `json:"errors"`
}

func (p *providers) verifyShellyOutput(ctx context.Context, device *shellyClient, expected bool) (shellySwitchStatus, error) {
	var status shellySwitchStatus
	var err error
	for attempt := 1; attempt <= shellyVerifyAttempts; attempt++ {
		status, err = device.verifyOutput(ctx, expected)
		if err == nil {
			return status, nil
		}
		if ctx.Err() != nil {
			return status, ctx.Err()
		}
		if attempt < shellyVerifyAttempts {
			if waitErr := p.sleep(ctx, shellyVerifyInterval); waitErr != nil {
				return status, fmt.Errorf("wait to retry Shelly verification: %w", waitErr)
			}
		}
	}
	return status, fmt.Errorf("Shelly verification failed after %d attempts: %w", shellyVerifyAttempts, err)
}

func (c *shellyClient) verifyOutput(ctx context.Context, expected bool) (shellySwitchStatus, error) {
	var status shellySwitchStatus
	if err := c.callRPC(ctx, "Switch.GetStatus", struct {
		ID int `json:"id"`
	}{ID: c.componentID}, &status); err != nil {
		return status, err
	}
	if status.ID == nil || *status.ID != c.componentID {
		return status, fmt.Errorf("Shelly status has a missing or incorrect component ID")
	}
	if status.Output == nil {
		return status, fmt.Errorf("Shelly status has no output")
	}
	if len(status.Errors) != 0 {
		return status, fmt.Errorf("Shelly status reports errors: %s", strings.Join(status.Errors, ", "))
	}
	if *status.Output != expected {
		return status, fmt.Errorf("Shelly output is %t, expected %t", *status.Output, expected)
	}
	return status, nil
}

func (c *shellyClient) callRPC(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{
		ID:     1,
		Method: method,
		Params: params,
	})
	if err != nil {
		return fmt.Errorf("encode Shelly command: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rpc", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Shelly request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call Shelly: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes))
	if err != nil {
		return fmt.Errorf("read Shelly response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Shelly returned HTTP %d", resp.StatusCode)
	}
	var response struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("decode Shelly response: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("Shelly rejected %s with code %d: %s", method, response.Error.Code, response.Error.Message)
	}
	if response.ID == nil || *response.ID != 1 {
		return fmt.Errorf("Shelly %s response has a missing or incorrect RPC ID", method)
	}
	if len(response.Result) == 0 || bytes.Equal(bytes.TrimSpace(response.Result), []byte("null")) {
		return fmt.Errorf("Shelly %s response has no result", method)
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode Shelly %s result: %w", method, err)
	}
	return nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
