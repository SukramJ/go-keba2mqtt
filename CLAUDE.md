# CLAUDE.md — AI Assistant Guide for go-keba2mqtt

## Project Overview

**go-keba2mqtt** is a pure-Go daemon that bridges a **KEBA KeContact P40 /
P40 Pro** wallbox to an **MQTT broker**, with **Home Assistant** MQTT
discovery and the shared **evcc** profile. It implements the KEBA *Modbus
TCP Programmers Guide* V1.03 (document 135655; the PDF lives in the
git-ignored `spec/` directory locally and is not redistributed): it polls
the readable registers, publishes them as mqtt-smarthome 2.0 status objects
(`<name>/status/…` as `{"val","ts","lc"}`, `<name>/set/…`,
`<name>/connected`, `<name>/info`, maintenance; openccu-loom ADR 0083) and
writes commands back.

It is a sibling of `go-mtec2mqtt` (whose architecture, tooling and
conventions it adopts) and `go-zendure2mqtt`, and it shares its Modbus
transport with go-mtec2mqtt through `github.com/SukramJ/go-modbus`.

## Key Characteristics

- **Language**: Go 1.27+ (see `go.mod` / CI `GO_VERSION`).
- **Module path**: `github.com/SukramJ/go-keba2mqtt`.
- **License: MIT.** Every Go source file starts with:
  ```go
  // SPDX-License-Identifier: MIT
  // Copyright (C) 2026 SukramJ
  ```
  The KEBA guide is © KEBA and must not be committed (`/spec/` is in
  `.gitignore`).
- **Deployment**: two static binaries (`CGO_ENABLED=0`): the `keba2mqtt`
  daemon and the `keba-util` CLI. Also a distroless Docker image, a Home
  Assistant add-on (`addon/`) and a systemd installer (`script/install.sh`).
- **Dependencies are deliberately minimal**: `github.com/SukramJ/go-modbus`
  (Modbus-TCP client), `github.com/SukramJ/go-mqtt` (MQTT 5 client),
  `github.com/SukramJ/go-hamqtt` (discovery model + publisher runtime),
  `github.com/SukramJ/go-ha-catalog` (HA vocabulary),
  `github.com/SukramJ/go-bridgeui` (the shared web dashboard), `golang.org/x/sync`,
  `gopkg.in/yaml.v3`. All SukramJ modules are pinned to exact versions.
- **Config**: YAML (`config-template.yaml` is the annotated reference),
  overridable per key via `KEBA_<KEY>` env vars. Loaded from `--config`,
  `./config.yaml`, `$XDG_CONFIG_HOME/keba2mqtt/config.yaml` (`$APPDATA` on
  Windows), `~/.config/keba2mqtt/config.yaml`, or env alone.

## The wallbox, in the facts that shape the code

- **One register per request, at most two words.** Readable registers are
  UINT32 (FC03, count 2), writable ones UINT16 (FC06). The two address sets
  are disjoint: 1000–1702 read, 5004–5200 write. There is no clustering.
- **Pacing**: ≥ 0.5 s between requests, ≥ 5 s between writes (guide §2).
  `keba.Device` enforces both through ONE gate for every caller (poll
  scheduler, write worker, web UI). A write waiting out its interval does
  not hold the gate, so reads continue; once it is due, reads yield to it
  (otherwise a back-to-back poll starves it — measured against a simulated
  P40). Never add a second path to the
  transport that bypasses `keba.Device`.
- **One Modbus client.** The P40 expects a single EMS. Integrations (evcc)
  go through MQTT, never in parallel over Modbus.
- **Writes have no direct readback.** Most write registers have a read
  counterpart (5004→1100, 5016→1600, 5018→1602, 5050→1550, 5052→1552); the
  catalog pairs them in one `keba.Item`. 5010 (energy limit) and 5014
  (enable) have none and are `Optimistic`: their status is the last value
  written. 5012 and 5200 are actions (buttons).
- **Firmware < 1.2.1** reports registers 1036/1502 in Wh instead of 0.1 Wh
  (guide §5). `keba.Decode` takes the raw firmware and corrects it.
- **Phase switching** (5052) works only when the source (5050) is 3
  (Modbus TCP); evcc's `phases1p3p` sets the source first.

## Repository Structure

```
cmd/keba2mqtt/         daemon entry point (composition root)
cmd/keba-util/         CLI: list / read / raw / write, directly over Modbus
internal/keba/         catalog (Items, Registers, Options), Decode/Encode, paced Device
internal/evcc/         the shared evcc profile: Derive (status A–F, …), command parsing, docs contract test
internal/hass/         topic functions (StateTopic, CommandTopic, CommandFilter, ConnectedTopic, OnlineTopic) + discovery document (RenderBundle)
internal/coordinator/  boot (static read, discovery), poll scheduler, write queue (coalescing, idempotent), evcc commands, keepalive, availability, web backend
internal/config/       YAML loader + KEBA_* env overlay + defaults + validation
internal/state/        thread-safe live-value cache for the web UI
internal/web/          go-bridgeui adapter: the coordinator's state as the shared dashboard's contract, page layout + labels (i18n/)
internal/version/      build info (Version/Commit/BuildDate via ldflags)
addon/                 Home Assistant add-on (manifest, Dockerfile, DOCS.md, CHANGELOG.md)
docs/evcc.md           evcc integration incl. the evcc.yaml snippet (checked by internal/evcc/docs_test.go)
script/                install.sh (curl|bash installer), run.sh (add-on entrypoint), extract-release-notes.sh
.github/workflows/     ci.yml, codeql.yml, docker-build-push.yml, addon-image.yml, release-on-tag.yml, dependabot-auto-merge.yml
```

## Development Commands

All in the `Makefile` (`make help`):

```sh
make build          # bin/keba2mqtt + bin/keba-util
make run            # build-daemon, run against ./config.yaml
make test           # go test -race -count=1 ./...
make check          # vet + fmt-check + lint + test — the pre-commit/pre-push gate
make lint           # golangci-lint
make vuln           # govulncheck
make licenses       # go-licenses: no GPL/AGPL/LGPL/MPL dependencies
make docker         # container image
make release        # cross-compiled archives + notes into dist/
make setup          # pinned tooling + git hooks
```

## Code Conventions

- **The catalog is Go, not YAML** (`internal/keba/catalog.go`): items need
  behaviour (paired registers, firmware-dependent scale, two value ranges)
  a data file cannot carry. Adding a register = adding an `Item`;
  `TestCatalogInvariants` guards keys and address ranges.
- **One function per topic** (`internal/hass`): the publisher, the command
  router and the discovery document all derive topics from the same
  functions; never concatenate a topic elsewhere.
- **Identity strings are constants**: `unique_id` = `keba_<serial>_<key>`,
  device identifier = bare serial, `default_entity_id` =
  `<platform>.<slug(DEVICE_NAME)|keba>_<key>`. Home Assistant has no
  migration path for them — changing any is a breaking release.
- **Item keys and enum tokens are API.** They are topic levels and `val`
  values that Home Assistant, evcc and user automations depend on.
- **The evcc profile is shared** with go-mtec2mqtt and go-zendure2mqtt
  (SukramJ/go-mtec2mqtt#75, SukramJ/go-zendure2mqtt#66). Change its keys,
  units or semantics only in all three projects together.
- **Writes**: every command becomes a `writeJob` keyed by its target
  register (coalescing), runs through `writeItem` (idempotency via
  `alreadyAt`, then a refresh read of the affected register).
- **golangci-lint v2** (`.golangci.yaml`), `gofumpt` + `goimports -local
  github.com/SukramJ/go-keba2mqtt`, `log/slog` only (`sloglint`).
- **Tests**: `internal/coordinator/coordinator_test.go` runs the whole
  daemon against a simulated P40 (`go-modbus/modbustest.Bank`) and an
  in-memory broker; extend it for behaviour that crosses packages.
- **Git hooks**: `make hooks` blocks direct commits on `main`/`master`.
- **Commit style**: Conventional Commits with a scope (`feat(evcc): …`,
  `fix(keba): …`, `chore(deps): …`).
- **Release bookkeeping — three files move together.** A version bump
  touches `internal/version/version.go` and `addon/config.yaml`, and every
  `changelog.md` entry is mirrored into `addon/CHANGELOG.md` (identical).

## When in Doubt

- [`README.md`](./README.md): topics, config, quickstart paths.
- [`docs/evcc.md`](./docs/evcc.md): the evcc profile.
- [`addon/DOCS.md`](./addon/DOCS.md): the add-on and its entities.
- [`config-template.yaml`](./config-template.yaml): every config key.
- The KEBA guide (local `spec/`) for register semantics.
