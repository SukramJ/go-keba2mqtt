# go-keba2mqtt add-on

## Before you start

Modbus TCP is **off by default** on the KeContact P40. Enable it in the KEBA
eMobility App (or via OCPP / REST API). Note the port (default `502`) and the
Unit ID (default `255`) shown there.

The P40 is meant to have **one** Modbus client. Do not let an energy manager
(or evcc's own KEBA driver) talk Modbus to it in parallel — let them use this
add-on's MQTT topics instead (see *evcc* below).

## Quickstart

1. Set **`modbus_ip`** to the wallbox's IP address.
2. Leave **`mqtt_server` empty** — the add-on connects to the Home Assistant
   MQTT broker by itself, and `hass_enable` is on by default, so the
   entities appear automatically.
3. **Start** the add-on and open its **Web UI** to watch the live values.

## Options reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `modbus_ip` | str | `0.0.0.0` | IP / hostname of the wallbox. **Required.** With the placeholder the add-on starts (the web UI stays reachable) but shows the wallbox as disconnected. |
| `modbus_port` | int | `502` | Modbus TCP port as configured on the wallbox. |
| `modbus_unit_id` | int | `255` | Unit ID as configured on the wallbox (0–255). |
| `modbus_timeout` | int | `5` | Timeout of one request, in seconds. |
| `mqtt_server` | str | `""` | Broker hostname. **Leave empty** to use the Home Assistant MQTT broker. |
| `mqtt_port` | int | `1883` | Broker port; only used with `mqtt_server`. |
| `mqtt_login` | str | `""` | Broker user; only used with `mqtt_server`. |
| `mqtt_password` | password | `""` | Broker password; only used with `mqtt_server`. |
| `mqtt_tls` | bool | `false` | Connect to `mqtt_server` via TLS (`tls://`); set `mqtt_port` to the TLS port too (usually `8883`). The auto-detected broker switches TLS on by itself when it reports it. See *TLS* below. |
| `mqtt_tls_ca` | str | `""` | PEM CA bundle to verify the broker against, e.g. `/ssl/ca.crt` or `/share/mqtt/ca.crt`. Empty = system roots. |
| `mqtt_tls_insecure` | bool | `false` | **Dangerous**: do not verify the broker certificate. Prefer `mqtt_tls_ca`; the two cannot be combined. |
| `mqtt_topic` | str | `keba` | Instance name, the first level of every topic. **Two wallboxes need two add-on instances with two names.** |
| `mqtt_maintenance` | bool | `true` | `<name>/maintenance/set/loglevel` and the retained `<name>/maintenance/stats`. The restart command is refused in the add-on; restart the add-on from Home Assistant. |
| `mqtt_stats_interval` | int | `60` | Seconds between `<name>/maintenance/stats`; `0` = off. |
| `hass_enable` | bool | `true` | Publish the Home Assistant discovery document. |
| `device_name` | str | `""` | Device name and entity-id seed: `Garage` → `sensor.garage_power`. Default name "KEBA KeContact P40", seed `keba`. The `unique_id` (`keba_<serial>_<key>`) never changes with it. |
| `rfid_enable` | bool | `false` | Publish the UID of the RFID card that authorised the session. Personal data, and reading it over Modbus must also be enabled on the wallbox. |
| `evcc_enable` | bool | `false` | Publish the evcc profile and accept its commands (see below). |
| `failsafe_keepalive` | int | `0` | Re-write the last commanded charging current every N seconds (0 = off, otherwise 5–600), so the wallbox's EMS failsafe engages only when commands really stop. Must be shorter than the failsafe timeout set on the wallbox. |
| `language` | list(en\|de) | `en` | Display language of entity names and the web UI. Entity ids never change with it. |
| `web_enable` | bool | `true` | Diagnostic web UI (required for the Ingress panel). |
| `debug` | bool | `false` | Verbose logging. |

Fixed by the add-on: the web UI binds `0.0.0.0:8080` for Ingress, the
discovery prefix is `homeassistant`, and the poll intervals and the request
pacing use the daemon defaults (one request per 0.5 s, one write per 5 s, as
the P40 requires). Run the standalone binary or the Docker image to tune them.

## TLS

- **Own broker:** set `mqtt_server`, `mqtt_tls: true` and `mqtt_port` to the
  TLS port (usually `8883`). The certificate is verified against the system
  roots.
- **Private CA:** copy the CA certificate (PEM) to `/ssl` or `/share` — both
  are mapped read-only into the add-on, e.g. with the Samba or File editor
  add-on — and set `mqtt_tls_ca` to its path, e.g. `/ssl/ca.crt`. A missing
  or unusable file stops the add-on at start with the file named in the log.
- **Auto-detected broker:** with `mqtt_server` empty the add-on connects via
  TLS when the Home Assistant MQTT service reports it; `mqtt_tls_ca` /
  `mqtt_tls_insecure` apply to it too.
- `mqtt_tls_insecure` skips certificate verification entirely — only for a
  broker you control, and only when a CA file is not an option.

The standalone binary and the Docker image use `MQTT_TLS`, `MQTT_TLS_CA` and
`MQTT_TLS_INSECURE` (env `KEBA_MQTT_TLS…`). `MQTT_SSL` / `MQTT_SSL_INSECURE`
were renamed; a configuration that still sets them is refused at start.

## Entities

| Entity | Notes |
| --- | --- |
| Charging state, cable state, error code | live state |
| Power, current L1–L3, voltage L1–L3, power factor | live measurements |
| Session energy, total energy | kWh, for the Energy dashboard |
| **Charging current limit** (number) | shows register 1100, sets 5004. `0` suspends charging, otherwise 6–32 A |
| **Phases** (select) | 1 / 3 phases. Takes effect only when *Phase switching source* is *Modbus TCP* |
| **Phase switching source** (select) | who may switch phases |
| **Failsafe current / timeout** (numbers) | the EMS failsafe: current applied when no Modbus command arrives within the timeout (0 = off) |
| **Charging station enabled** (switch) | 5014; the wallbox reports no state for it, so the switch shows the last value set |
| **Session energy limit** (number) | ends the session after N kWh (0 = no limit); shows the last value set |
| **Unlock plug**, **Activate fast charging** (buttons) | fast charging cannot be deactivated over Modbus |
| Fast charging, max supported current, model, firmware, … | diagnostics |

Every write is paced by the wallbox's 5-second write interval and skipped
when the wallbox already reports the requested value.

Firmware below 1.2.1 reports the two energy registers in Wh instead of
0.1 Wh (KEBA's known bug); the add-on corrects that automatically.

## MQTT topics

[mqtt-smarthome 2.0](https://github.com/mqtt-smarthome/mqtt-smarthome/blob/master/SPEC.md),
`<name>` = `mqtt_topic`:

| Topic | |
| --- | --- |
| `<name>/status/<serial>/<group>/<key>` | `{"val":…,"ts":…,"lc":…}`, retained. Groups `now`, `config`, `static` |
| `<name>/set/<serial>/<group>/<key>` | plain value or `{"val":…}` |
| `<name>/status/<serial>/online` | the wallbox answers Modbus |
| `<name>/connected` | `2` wallbox reachable, `1` broker only, `0` add-on down |
| `<name>/info`, `<name>/maintenance/…` | instance info and maintenance |

**Security:** anyone who may publish on the broker can send commands and
change the log level. The Mosquitto add-on is authenticated; on a broker you
cannot secure, set `mqtt_maintenance: false`.

## evcc

With `evcc_enable: true` the add-on publishes the **shared evcc profile** —
the same surface go-mtec2mqtt and go-zendure2mqtt expose — under
`<name>/status/<serial>/evcc/…` and accepts evcc's charger commands under
`<name>/set/<serial>/evcc/…`. evcc then controls the wallbox through MQTT as
a `custom` charger, and the add-on remains the only Modbus client.

The complete `evcc.yaml` snippet is in
[docs/evcc.md](https://github.com/SukramJ/go-keba2mqtt/blob/main/docs/evcc.md).

## Home Assistant discovery

One retained device document at `homeassistant/device/<serial>/config`
(**Home Assistant 2024.11 or newer**). The add-on log prints the exact topic
at start-up (`coordinator.discovery_bundle_built topic=…`).
