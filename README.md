# go-keba2mqtt

[![Open your Home Assistant instance and add this add-on repository.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FSukramJ%2Fgo-keba2mqtt)

A pure-Go bridge between a **KEBA KeContact P40 / P40 Pro** wallbox and an
**MQTT broker**, with **Home Assistant** auto-discovery and an **evcc**
profile. It speaks the wallbox's Modbus TCP interface
(KEBA *Modbus TCP Programmers Guide* V1.03, available from
[keba.com/emobility-downloads](https://www.keba.com/emobility-downloads)), publishes every value
as an [mqtt-smarthome 2.0](https://github.com/mqtt-smarthome/mqtt-smarthome/blob/master/SPEC.md)
status object and writes commands back — charging current, phases, energy
limit, failsafe, unlock, fast charging.

Sibling of [go-mtec2mqtt](https://github.com/SukramJ/go-mtec2mqtt) and
[go-zendure2mqtt](https://github.com/SukramJ/go-zendure2mqtt): same project
setup, same topic convention, same evcc profile, and the Modbus transport
shared through [go-modbus](https://github.com/SukramJ/go-modbus).

## Features

- **Every register of the P40 guide**: charging and cable state, error code,
  power, currents, voltages, power factor, session and total energy, current
  limits, fast charging, phase switching, failsafe settings, serial, product
  features, firmware and hardware revisions — plus the RFID card UID as an
  opt-in.
- **Writes, paced and idempotent**: the P40 takes one request per 0.5 s and
  one write per 5 s. Every caller (polling, Home Assistant, evcc, web UI) goes
  through one gate that enforces both; a command the wallbox already reports
  is not written again, and a burst of commands collapses to the last one.
- **Home Assistant discovery** as one retained device document: sensors,
  numbers (current limit, energy limit, failsafe), selects (phases, phase
  switching source), a switch (enable station) and buttons (unlock plug,
  fast charging), German or English names, `unique_id`s keyed on the serial
  so several wallboxes coexist.
- **evcc profile** (opt-in): charger status A–F, enable, max current (A and
  mA precision), 1p/3p phase switching and RFID identification under
  `<name>/…/evcc/…` — see [docs/evcc.md](./docs/evcc.md).
- **Failsafe keepalive** (opt-in): re-writes the commanded current so the
  wallbox's EMS failsafe only engages when the controller really stops.
- **Firmware quirk handled**: below 1.2.1 the energy registers report Wh
  instead of 0.1 Wh; the bridge reads the firmware and corrects it.
- **Diagnostic web UI** (optional, Home Assistant Ingress-friendly), a
  `keba-util` CLI, a distroless Docker image, a Home Assistant add-on and a
  systemd installer.
- **Pure Go**, `CGO_ENABLED=0`, static binaries.

## Before you start

Modbus TCP is **off by default** on the P40. Enable it in the KEBA
eMobility App (or via OCPP / REST API) and note port (default `502`) and
Unit ID (default `255`). The P40 is meant to have **one** Modbus client:
do not run an energy manager's own KEBA Modbus driver in parallel — point
it at this bridge's MQTT topics instead.

## Quickstart

### Linux (Ubuntu / Raspberry Pi OS)

Downloads the latest release, verifies its checksum, installs under
`/opt/go-keba2mqtt`, creates a `keba` service user, asks for the wallbox
address, the broker and whether to enable Home Assistant discovery, and
registers a hardened systemd unit:

```bash
curl -sSfL https://raw.githubusercontent.com/SukramJ/go-keba2mqtt/main/script/install.sh | sudo bash
```

```bash
sudo systemctl status go-keba2mqtt
journalctl -u go-keba2mqtt -f
sudo nano /etc/go-keba2mqtt/config.yaml
sudo systemctl restart go-keba2mqtt
```

### Docker

```bash
docker run --rm -d --name keba2mqtt \
  -e KEBA_MODBUS_IP=192.168.1.50 \
  -e KEBA_MQTT_SERVER=192.168.1.10 \
  -e KEBA_HASS_ENABLE=true \
  ghcr.io/sukramj/go-keba2mqtt:latest
```

Or mount a config: `-v /path/to/config:/config:ro` with
`/path/to/config/keba2mqtt/config.yaml` (start from
[`config-template.yaml`](./config-template.yaml)).

### Home Assistant add-on

1. **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, add
   `https://github.com/SukramJ/go-keba2mqtt`.
2. Install **go-keba2mqtt**, set `modbus_ip` (leave `mqtt_server` empty to
   use the Home Assistant broker) and start it.

Details: [`addon/DOCS.md`](./addon/DOCS.md).

### Binary

```bash
make build
./bin/keba2mqtt --config ./config.yaml
```

Without `--config` the daemon looks for `./config.yaml`,
`$XDG_CONFIG_HOME/keba2mqtt/config.yaml` (`%APPDATA%\keba2mqtt\…` on
Windows) and `~/.config/keba2mqtt/config.yaml`, and otherwise runs from
`KEBA_*` environment variables alone.

### CLI

```bash
./bin/keba-util list                          # the catalog: keys, registers, units
./bin/keba-util --host 192.168.1.50 read      # every value
./bin/keba-util read now                      # one group, or individual keys
./bin/keba-util write charging_current 16     # A; 0 suspends
./bin/keba-util write phases "1 phase"
./bin/keba-util raw 1000 1016                 # raw UINT32 registers
```

Stop the daemon first — the wallbox expects a single Modbus client.

## Configuration

[`config-template.yaml`](./config-template.yaml) documents every key; each
can be overridden by `KEBA_<KEY>`.

| Key | Default | |
| --- | --- | --- |
| `MODBUS_IP` | — | wallbox address (**required**) |
| `MODBUS_PORT` / `MODBUS_UNIT_ID` | `502` / `255` | as configured on the wallbox |
| `MODBUS_TIMEOUT` | `5` | seconds per request |
| `MODBUS_READ_INTERVAL` / `MODBUS_WRITE_INTERVAL` | `500` ms / `5` s | request pacing required by the P40 |
| `MQTT_SERVER` | — | broker (**required**) |
| `MQTT_PORT`, `MQTT_LOGIN`, `MQTT_PASSWORD`, `MQTT_SSL`, `MQTT_SSL_INSECURE` | `1883` | broker connection; TLS opt-in |
| `MQTT_TOPIC` | `keba` | instance name; one per wallbox |
| `MQTT_MAINTENANCE` / `MQTT_STATS_INTERVAL` | `true` / `60` | mqtt-smarthome maintenance topics |
| `HASS_ENABLE`, `HASS_BASE_TOPIC`, `HASS_BIRTH_GRACETIME` | `false`, `homeassistant`, `15` | discovery |
| `DEVICE_NAME` | `""` | device name and entity-id seed |
| `REFRESH_NOW` / `REFRESH_CONFIG` / `REFRESH_STATIC` | `10` / `60` / `3600` | poll intervals (s) |
| `RFID_ENABLE` | `false` | publish the RFID card UID |
| `EVCC_ENABLE` | `false` | the evcc profile |
| `FAILSAFE_KEEPALIVE` | `0` | re-write the commanded current every N s |
| `WEB_ENABLE`, `WEB_BIND`, `WEB_USER`, `WEB_PASSWORD` | `false`, `127.0.0.1:8080` | diagnostic web UI |
| `LANGUAGE` | `en` | `en` or `de` |
| `DEBUG` | `false` | verbose logging |

## MQTT topics

`<name>` is `MQTT_TOPIC`, `<serial>` the wallbox serial number.

| Topic | |
| --- | --- |
| `<name>/status/<serial>/now/<key>` | live values, every `REFRESH_NOW` |
| `<name>/status/<serial>/config/<key>` | settings, every `REFRESH_CONFIG` |
| `<name>/status/<serial>/static/<key>` | identity, every `REFRESH_STATIC` |
| `<name>/status/<serial>/evcc/<key>` | the evcc profile (`EVCC_ENABLE`) |
| `<name>/set/<serial>/<group>/<key>` | commands |
| `<name>/status/<serial>/online` | `true` while the wallbox answers |
| `<name>/connected` | `2` wallbox reachable · `1` broker only · `0` bridge down (Last Will) |
| `<name>/info`, `<name>/maintenance/…` | instance info, log level, restart, stats |

Every status value is a retained `{"val": …, "ts": …, "lc": …}` object;
`val` is a JSON number, boolean or string. Enumerations carry their English
token (`"Charging"`, `"3 phases"`), and Home Assistant shows the label of
the configured language. A `set` takes a plain value or `{"val": …}`;
selects also accept the German label.

`keba-util list` prints every key. The writable ones:

| Key | Group | Values |
| --- | --- | --- |
| `charging_current` | now | A: `0` (suspend) or `6`–`32` |
| `phases` | now | `1 phase`, `3 phases` (needs phase switching source *Modbus TCP*) |
| `activate_fast_charging` | now | any payload (cannot be deactivated over Modbus) |
| `phase_switch_source` | config | `Disabled`, `OCPP / profiles`, `REST API`, `Modbus TCP` |
| `failsafe_current` | config | A: `0` or `6`–`32` |
| `failsafe_timeout` | config | s: `0` (off) or `5`–`600` |
| `charging_enabled` | config | `true` / `false` |
| `energy_limit` | config | kWh, `0` = no limit, 0.01 kWh steps |
| `unlock_plug` | config | any payload |

`charging_enabled` and `energy_limit` have no read register; their status
is the last value written successfully.

## Development

```bash
make setup     # pinned tooling + git hooks
make check     # vet + gofumpt + golangci-lint + tests (pre-push gate)
make build     # bin/keba2mqtt, bin/keba-util
make docker    # container image
```

```
cmd/keba2mqtt/        daemon entry point
cmd/keba-util/        CLI: list / read / raw / write
internal/keba/        the P40 catalog, decoding, paced device access (over go-modbus)
internal/evcc/        the shared evcc profile: derivations and command parsing
internal/hass/        topic schema + Home Assistant discovery document (go-hamqtt)
internal/coordinator/ boot, poll scheduler, write queue, evcc commands, keepalive, availability
internal/config/      YAML + KEBA_* env + validation
internal/state/       live value cache for the web UI
internal/web/         web UI: adapter to the shared go-bridgeui dashboard
```

Tests run the coordinator end to end against a simulated P40
(`go-modbus/modbustest`) and an in-memory broker.

Parts of go-keba2mqtt are developed with agentic AI assistance, primarily
[Claude Code](https://www.anthropic.com/claude-code). Every change is
reviewed by a human maintainer and has to pass the test suite. AI-assisted
contributions are welcome under the [AI contribution policy](./AI_POLICY.md).

## Compatibility

| Component | Status |
| --- | --- |
| Wallbox | KEBA KeContact P40, P40 Pro with Modbus TCP enabled (guide V1.03). Phase switching, the enable register and fast charging need a current wallbox firmware (see the guide's revision record). |
| MQTT | MQTT 5.0 (3.1.1 selectable in go-mqtt), plain or TLS |
| Home Assistant | 2024.11 or newer (device-based discovery) |
| Go | 1.27+ |

## License

MIT — see [LICENSE](./LICENSE). KEBA and KeContact are trademarks of KEBA;
this project is not affiliated with KEBA. The programmers guide it implements
is © KEBA and not redistributed here.
