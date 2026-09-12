# switch-api

`switch-api` is a small, authenticated HTTP API for controlling devices through
configured ON and OFF sequences. A logical switch can represent a printer,
amplifier, PC, light, or any other device without exposing its provider details
to API callers.

If you want to print documents on a printer through an HTTP API, see
[print-api](https://github.com/mu373/print-api).

## Features

- Named logical switches with independent ON and OFF sequences
- Serial execution of configured steps
- Per-switch operation locking to prevent overlapping actions
- SwitchBot Cloud API v1.1 support
- Optional Shelly Gen2+ local RPC support
- Delay steps between device actions
- API-key authentication, OpenAPI 3.0, and embedded Swagger UI
- Static Linux `amd64` builds with CGO disabled

## Supported step drivers

| Driver | Actions | Purpose |
| --- | --- | --- |
| `switchbot` | `turnOn`, `turnOff`, `press` | Send a SwitchBot Bot command. |
| `shelly-gen2` | `on`, `off`, `verify-on`, `verify-off` | Set the relay output or confirm its state through local RPC. |
| `delay` | — | Wait for a configured duration. |

An optional SwitchBot or Shelly step is reported as `skipped` only when its
provider or device is not configured. Once configured, a provider failure stops
the sequence and returns an error; it is never silently ignored.

## Configuration

YAML is the standard configuration format. Unknown fields are rejected during
startup. JSON remains accepted for backward compatibility.

```bash
cp config.example.yaml config.yaml
cp .env.example .env
chmod 600 .env
```

Set the API key and SwitchBot credentials in `.env`.

```dotenv
# Used by clients calling switch-api.
SWITCH_CONTROL_API_KEY=replace-with-a-long-random-value

# Obtained from SwitchBot Developer Options.
SWITCHBOT_TOKEN=...
SWITCHBOT_SECRET=...

SWITCH_API_CONFIG=config.yaml
```

Each logical switch defines exactly which devices and actions are used. A
switch does not automatically use Shelly or SwitchBot.

```yaml
switches:
  - id: office-printer
    display_name: Office printer
    on:
      - driver: switchbot
        device_id: REPLACE_WITH_SWITCHBOT_DEVICE_ID
        action: turnOn
    off:
      - driver: switchbot
        device_id: REPLACE_WITH_SWITCHBOT_DEVICE_ID
        action: turnOff
```

### Add Shelly to one switch

Register only the Shelly devices that exist in your environment.

```yaml
shelly_devices:
  printer-plug:
    base_url: http://192.168.1.50
    component_id: 0

switches:
  - id: office-printer
    on:
      - driver: shelly-gen2
        device_id: printer-plug
        action: on
      - driver: shelly-gen2
        device_id: printer-plug
        action: verify-on
      - driver: delay
        duration: 2s
      - driver: switchbot
        device_id: REPLACE_WITH_SWITCHBOT_DEVICE_ID
        action: turnOn
    off:
      - driver: switchbot
        device_id: REPLACE_WITH_SWITCHBOT_DEVICE_ID
        action: turnOff
      - driver: delay
        duration: 15s
      - driver: shelly-gen2
        device_id: printer-plug
        action: off
      - driver: shelly-gen2
        device_id: printer-plug
        action: verify-off
```

`verify-on` and `verify-off` call `Switch.GetStatus` and require the configured
component's `output` to match the expected state. Verification makes up to three
attempts, waiting one second between failed attempts. A persistent mismatch, reported
device error, invalid response, or failed request stops the sequence after those
attempts. Cancellation or the action timeout ends retries immediately. Verification
does not change the relay state or retry commands. Its step result includes `output`
and, when the device provides it, `power_watts`. Relay output confirms electrical
supply; printer readiness is checked separately by the printing workflow.

The current Shelly driver targets unauthenticated Gen2+ RPC endpoints on a
trusted local network. Add Digest authentication support before using it with a
Shelly device that has HTTP authentication enabled.

## Run

```bash
set -a
source .env
set +a
./dist/switch-api
```

The default listen address is `:8010`. Set `listen_addr` in `config.yaml` to
use another address or port, for example `127.0.0.1:9010` for localhost only or
`:9010` for all interfaces. The service currently has no command-line address
or port flag.

## API

All switch endpoints require `X-Api-Key` with `SWITCH_CONTROL_API_KEY`.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Unauthenticated process health check. |
| `GET` | `/switches` | List configured logical switches. |
| `POST` | `/switches/{id}/on` | Execute the configured ON sequence. |
| `POST` | `/switches/{id}/off` | Execute the configured OFF sequence. |

```bash
curl \
  -H "X-Api-Key: $SWITCH_CONTROL_API_KEY" \
  -X POST \
  http://localhost:8010/switches/office-printer/on
```

The response contains an ordered result for every step.

```json
{
  "switch_id": "office-printer",
  "requested_state": "on",
  "status": "completed",
  "steps": [
    {
      "index": 0,
      "driver": "switchbot",
      "device_id": "...",
      "action": "turnOn",
      "status": "completed"
    }
  ]
}
```

## OpenAPI and Swagger UI

When the service is running:

- OpenAPI document: `http://localhost:8010/openapi.yaml`
- Swagger UI: `http://localhost:8010/docs/`

Swagger UI assets are embedded in the binary and do not require browser access
to the public internet.

## Build and test

The Makefile pins Go `1.25.3`, Linux `amd64`, and `CGO_ENABLED=0` for release
builds.

```bash
make check
make test
make race
make build
make checksum
```

The release binary is written to `dist/switch-api`.

## systemd

An example unit is provided in `switch-api.service`.

```bash
sudo useradd --system --no-create-home switch-api
sudo mkdir -p /opt/switch-api
sudo cp dist/switch-api config.yaml /opt/switch-api/
sudo cp .env /opt/switch-api/.env
sudo chown -R switch-api:switch-api /opt/switch-api
sudo chmod 600 /opt/switch-api/.env /opt/switch-api/config.yaml
sudo cp switch-api.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now switch-api
```

## License

MIT License
