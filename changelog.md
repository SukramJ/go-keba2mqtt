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
