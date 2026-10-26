# evcc

go-keba2mqtt exposes the **shared evcc profile** — one MQTT surface that
go-keba2mqtt, [go-mtec2mqtt](https://github.com/SukramJ/go-mtec2mqtt) and
[go-zendure2mqtt](https://github.com/SukramJ/go-zendure2mqtt) implement
identically, so an `evcc.yaml` reads the same for every device. evcc drives
the wallbox as a `custom` charger over MQTT, and the bridge remains the
wallbox's only Modbus client — the P40 is meant to have exactly one.

Enable it with `EVCC_ENABLE: true` (env `KEBA_EVCC_ENABLE`, add-on option
`evcc_enable`).

## Topics

```
<name>/status/<serial>/evcc/<key>    {"val": …, "ts": …, "lc": …}   retained
<name>/set/<serial>/evcc/<key>       plain value (or {"val": …})
```

`<name>` is `MQTT_TOPIC` (`keba`), `<serial>` the wallbox serial number (in
the log at start-up, `coordinator.static_initialised serial=…`, and on the
Home Assistant device page). evcc reads every status value with `jq: .val`.

| Status key | Unit | Meaning |
| --- | --- | --- |
| `status` | — | `A` no vehicle · `B` connected · `C` charging · `E` error · `F` wallbox starting up |
| `enabled` | bool | a charging current is set (register 1100 > 0) |
| `maxcurrent` | A | the current limit the wallbox reports |
| `phases` | 1 / 3 | phases in use |
| `power` | W | active power, positive while charging |
| `energy` | kWh | total meter reading |
| `current_l1..l3` | A | phase currents |
| `voltage_l1..l3` | V | phase voltages |
| `identify` | — | RFID UID, only with `RFID_ENABLE` |

| Command key | Payload | Effect |
| --- | --- | --- |
| `enable` | `true` / `false` | `false` sets the charging current to 0 (suspends); `true` restores the last `maxcurrent` |
| `maxcurrent` | integer A | 6–32; remembered while disabled, written while enabled |
| `maxcurrentmillis` | float A | as `maxcurrent`, with decimals |
| `phases1p3p` | `1` / `3` | switches phases; sets the phase switching source to *Modbus TCP* first when needed |

evcc re-sends its charger state every cycle. The bridge writes only what
changes: a command that matches what the wallbox already reports costs
nothing, and commands that arrive faster than the wallbox's 5-second write
interval are coalesced so that only the latest one is written.

## evcc.yaml

Replace `18416854` with your wallbox serial and `keba` with your
`MQTT_TOPIC` if you changed it.

```yaml
mqtt:
  broker: localhost:1883
  topic: evcc

chargers:
  - name: keba_p40
    type: custom
    status:
      source: mqtt
      topic: keba/status/18416854/evcc/status
      jq: .val
    enabled:
      source: mqtt
      topic: keba/status/18416854/evcc/enabled
      jq: .val
    enable:
      source: mqtt
      topic: keba/set/18416854/evcc/enable
    maxcurrent:
      source: mqtt
      topic: keba/set/18416854/evcc/maxcurrent
    maxcurrentmillis:
      source: mqtt
      topic: keba/set/18416854/evcc/maxcurrentmillis
    phases1p3p:
      source: mqtt
      topic: keba/set/18416854/evcc/phases1p3p
    power:
      source: mqtt
      topic: keba/status/18416854/evcc/power
      jq: .val
    energy:
      source: mqtt
      topic: keba/status/18416854/evcc/energy
      jq: .val
    currents:
      - source: mqtt
        topic: keba/status/18416854/evcc/current_l1
        jq: .val
      - source: mqtt
        topic: keba/status/18416854/evcc/current_l2
        jq: .val
      - source: mqtt
        topic: keba/status/18416854/evcc/current_l3
        jq: .val
    voltages:
      - source: mqtt
        topic: keba/status/18416854/evcc/voltage_l1
        jq: .val
      - source: mqtt
        topic: keba/status/18416854/evcc/voltage_l2
        jq: .val
      - source: mqtt
        topic: keba/status/18416854/evcc/voltage_l3
        jq: .val
    # Only with RFID_ENABLE: true
    identify:
      source: mqtt
      topic: keba/status/18416854/evcc/identify
      jq: .val

loadpoints:
  - title: Garage
    charger: keba_p40
    mode: pv
```

Leave out `phases1p3p` if your P40 has no phase switching (product feature
"3/1-phase switching"; the bridge publishes the decoded feature list as the
`model` value).

## Freshness and failure

- The snippet sets no `timeout`: the bridge publishes a value only when it
  changes, so a steady value (no vehicle, 0 W for hours) would look outdated
  to evcc and fail the charger. Availability is `keba/connected` instead:
  `2` while the bridge reaches the wallbox, `1` when it does not, `0` when
  the bridge is down.
- The wallbox's **EMS failsafe** (registers 1600/1602, *Failsafe current* and
  *Failsafe timeout* in Home Assistant) applies a fallback current when no
  Modbus command arrives within the timeout. evcc sends nothing while its
  state does not change, and the bridge skips unchanged writes, so set
  `FAILSAFE_KEEPALIVE` below the wallbox's failsafe timeout: the bridge then
  re-writes the last commanded current periodically, and the failsafe
  engages only when the bridge itself stops.

## The shared profile

The same contract holds for all three bridges (go-mtec2mqtt,
go-zendure2mqtt): the `evcc` item group, evcc's units and signs (power
positive = import / production / discharge / consumption; `energy` the
imported / produced / discharged / charged meter reading, `returnenergy`
the exported or charged one where a meter has it), `EVCC_ENABLE` off by
default, idempotent commands, and a `docs/evcc.md` whose snippet is
checked against the topics the bridge really publishes
(`internal/evcc/docs_test.go`). Devices with several roles nest them one
level deeper (`evcc/grid/power`, `evcc/battery/soc`); a wallbox has one role
and publishes directly under `evcc/`.
