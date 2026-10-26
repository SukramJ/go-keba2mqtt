# Unreleased

## What's Changed

### Breaking
- **`MQTT_SSL` / `MQTT_SSL_INSECURE` are renamed to `MQTT_TLS` /
  `MQTT_TLS_INSECURE`.** Migration: rename the keys (env
  `KEBA_MQTT_SSL*` → `KEBA_MQTT_TLS*`). A configuration that still sets
  an old key — in the file or the environment, even set to `false` — is
  refused at start with `MQTT_SSL was renamed to MQTT_TLS`, because a
  silently ignored `MQTT_SSL: true` would fall back to plain TCP.

### Added
- **Private CA for the broker: `MQTT_TLS_CA`.** A PEM CA bundle the
  broker certificate is verified against instead of the system roots.
  An unusable file stops the daemon at start with the file named.
  `MQTT_TLS_CA` together with `MQTT_TLS_INSECURE` is a configuration
  error; either one without `MQTT_TLS` has no effect and is warned about.
  `MQTT_TLS_INSECURE` now logs a warning at start.
- **Add-on: TLS options.** `mqtt_tls`, `mqtt_tls_ca` and
  `mqtt_tls_insecure`; `/ssl` and `/share` are mapped read-only for the
  CA file. With `mqtt_server` empty, the auto-detected Home Assistant
  MQTT service switches TLS on when it reports it.
- go-mqtt 1.7.0.

### Changed

- go-bridgeui v0.3.0: the dashboard translates value names and problem
  events through the bridge's labels, and a wide card no longer pushes the
  page past the window.

# Version 0.2.0 (2026-10-26)

## What's Changed

### Changed
- **The web UI comes from go-bridgeui.** The dashboard is now the shared
  [go-bridgeui](https://github.com/SukramJ/go-bridgeui) module (v0.1.0),
  the same one go-mtec2mqtt and go-zendure2mqtt embed. It looks and works
  as before. Two things are new: the browser loads the new UI after an
  update (content-hash ETag), and the evcc section and its menu entry stay
  hidden while the profile is off.
- **API.** `/api/registers` is replaced by `/api/items` (the writable
  items with their controls); `/api/health`, `/api/status`, `/api/snapshot`
  and `/api/config` now have go-bridgeui's shape, and `/api/ui` is new.
  `POST /api/write` still takes `{"key","value"}`. An unknown key now
  answers 400 instead of 404.
- MQTT and Home Assistant are unchanged.

# Version 0.1.0 (2026-10-25)

## What's Changed

### Added

- **First release.** Bridges a KEBA KeContact P40 / P40 Pro to MQTT over
  Modbus TCP, following the KEBA Modbus TCP Programmers Guide V1.03.
- Every readable register as an mqtt-smarthome 2.0 status object under
  `keba/status/<serial>/{now,config,static}/…`; every writable register
  through `keba/set/<serial>/…` — charging current, phases, phase switching
  source, failsafe current and timeout, station enable, session energy
  limit, unlock plug, fast charging.
- Request pacing as the P40 requires — one request per 0.5 s, one write per
  5 s — across polling, Home Assistant, evcc and the web UI; writes the
  wallbox already reports are skipped, bursts collapse to the last command.
- Home Assistant MQTT discovery as one retained device document, English or
  German names, `unique_id`s keyed on the serial.
- The shared evcc profile (`EVCC_ENABLE`): charger status A–F, enable, max
  current, 1p/3p switching, RFID identification; see `docs/evcc.md`.
- `FAILSAFE_KEEPALIVE`: periodic re-write of the commanded current for the
  wallbox's EMS failsafe.
- Opt-in RFID card UID (`RFID_ENABLE`).
- Correction of the energy registers on firmware below 1.2.1 (Wh instead of
  0.1 Wh).
- Diagnostic web UI, `keba-util` CLI, distroless Docker image, Home
  Assistant add-on, systemd installer.
