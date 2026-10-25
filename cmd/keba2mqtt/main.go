// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command keba2mqtt is the KEBA KeContact P40 → MQTT bridge daemon.
//
// It loads its configuration from $XDG_CONFIG_HOME/keba2mqtt/config.yaml
// (or --config, or KEBA_* environment variables alone), connects to the
// wallbox over Modbus TCP and to an MQTT broker, publishes the wallbox's
// values as mqtt-smarthome 2.0 status objects with optional Home
// Assistant discovery and the shared evcc profile, and writes commands
// back to the wallbox.
//
// SIGINT/SIGTERM cancel every in-flight transaction before exiting.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-modbus"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-keba2mqtt/internal/config"
	"github.com/SukramJ/go-keba2mqtt/internal/coordinator"
	"github.com/SukramJ/go-keba2mqtt/internal/hass"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
	"github.com/SukramJ/go-keba2mqtt/internal/state"
	"github.com/SukramJ/go-keba2mqtt/internal/version"
	"github.com/SukramJ/go-keba2mqtt/internal/web"
)

const clientIDBase = "keba2mqtt-"

func main() {
	configPath := flag.String("config", "",
		"explicit config.yaml path (defaults to the standard search order)")
	showVersion := flag.Bool("version", false, "print build info and exit")
	healthcheck := flag.Bool("healthcheck", false,
		"probe the local web UI health endpoint and exit 0 (healthy) or 1; container HEALTHCHECK hook")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck(*configPath))
	}

	// One level variable behind the one handler, so DEBUG and the
	// `maintenance/set/loglevel` command move the level actually logged at.
	level := &slog.LevelVar{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	logger.Info("keba2mqtt.boot", slog.String("build", version.String()))

	if err := run(*configPath, logger, level); fatalErr(err) {
		logger.Error("keba2mqtt.fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

// fatalErr reports whether err is a genuine failure rather than the
// normal outcome of a cancelled run context — a SIGTERM during start-up
// must not make systemd record a clean stop as a failure.
func fatalErr(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}

// run is the testable entry point.
func run(configPath string, logger *slog.Logger, level *slog.LevelVar) error {
	cfg, err := loadConfig(configPath, logger)
	if err != nil {
		return err
	}
	if cfg.Debug {
		level.Set(slog.LevelDebug)
	}
	if layout := hass.NewLayout(cfg.MQTTTopic); !layout.Conformant() {
		logger.Warn("keba2mqtt.mqtt_topic_multi_level",
			slog.String("mqtt_topic", cfg.MQTTTopic),
			slog.String("effect", "runs outside mqtt-smarthome 2.0 §3; invisible to a +/info scan"))
	}

	// cancel is also the maintenance `restart` path: the same graceful
	// shutdown a SIGTERM takes, and the supervisor starts the process again.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- wallbox ---
	device := keba.NewDevice(modbus.New(modbus.Config{
		Host:    cfg.ModbusIP,
		Port:    cfg.ModbusPort,
		UnitID:  cfg.ModbusUnitID,
		Timeout: cfg.ModbusTimeoutDuration(),
		Logger:  logger,
	}), keba.WithIntervals(cfg.ModbusReadIntervalDuration(), cfg.ModbusWriteIntervalDuration()))
	catalog := keba.NewCatalog(cfg.RFIDEnable)

	// --- Home Assistant plane ---
	//
	// Built BEFORE the MQTT client, because the Last Will is part of
	// CONNECT and the will is this runtime's statement: Will() returns the
	// topic and payload AnnounceOnline/AnnounceOffline write, the
	// `<name>/connected` every entity reads its bridge availability from.
	// A factory, because the runtime's memory is per connection; the
	// coordinator builds a fresh one on every (re)connect.
	haLink := &deferredTransport{}
	haConfig := coordinator.HARuntimeConfig(cfg, logger)
	newHARuntime := func() *publisher.Runtime { return publisher.New(haLink, haConfig) }
	bootRuntime := newHARuntime()
	defer bootRuntime.Close()
	will, err := bootRuntime.Will()
	if err != nil {
		return fmt.Errorf("keba2mqtt: mqtt will: %w", err)
	}

	// --- mqtt ---
	var tlsConfig *tls.Config
	if cfg.MQTTSSL {
		tlsConfig = mqtt.NewClientTLSConfig(cfg.MQTTServer, cfg.MQTTSSLInsecure)
	}
	mqttClient := mqtt.NewTCPClient(mqtt.TCPConfig{
		BrokerURL:  cfg.MQTTBrokerURL(),
		ClientID:   clientIDBase + strings.ReplaceAll(cfg.MQTTTopic, "/", "-"),
		Username:   cfg.MQTTLogin,
		Password:   cfg.MQTTPassword,
		KeepAlive:  60 * time.Second,
		CleanStart: true,
		Will: &mqtt.Will{
			Topic:   will.Topic,
			Payload: will.Payload,
			QoS:     mqtt.QoS(will.QoS),
			Retain:  will.Retain,
		},
		TLSConfig: tlsConfig,
		Logger:    logger,
	})
	mqttLifecycle := mqtt.NewLifecycle(mqtt.DefaultLifecycle(), mqttClient)
	// Publishes fail fast while the broker is degraded; subscribes go
	// around the breaker.
	breaker := mqtt.NewBreaker(mqttClient, mqtt.BreakerConfig{
		OnStateChange: func(from, to mqtt.BreakerState) {
			logger.Warn("keba2mqtt.mqtt_breaker_state",
				slog.String("from", from.String()), slog.String("to", to.String()))
		},
	})
	haLink.wire(hagomqtt.Split(breaker, mqttClient))

	// --- state plane: {"val","ts","lc"} status objects, retained, QoS 0 ---
	statePlane := publisher.StateFor(bootRuntime, publisher.StateConfig{
		QoS:            coordinator.StateQoS,
		PulseQoS:       coordinator.StateQoS,
		Encoding:       discovery.StatusObjectEncoding,
		CommandFilters: []string{hass.CommandFilter(cfg.MQTTTopic)},
		Logger:         logger,
	})

	// --- `<name>/info` and the maintenance topics ---
	instance := publisher.NewInstance(haLink, publisher.InstanceConfig{
		Layout:              hass.NewLayout(cfg.MQTTTopic),
		Name:                hass.OriginName,
		Version:             version.Version,
		Extra:               instanceInfo(cfg),
		MaintenanceDisabled: !cfg.MQTTMaintenance,
		SetLogLevel:         publisher.LevelVarSetter(level),
		Supervised:          publisher.DetectSupervised(config.EnvPrefix + "SUPERVISED"),
		Shutdown:            cancel,
		StatsInterval:       publisher.StatsInterval(cfg.MQTTStatsInterval),
		Logger:              logger,
	})

	var store *state.Store
	if cfg.WebEnable {
		store = state.New()
	}

	c := coordinator.New(coordinator.Deps{
		Cfg:          cfg,
		Catalog:      catalog,
		Device:       device,
		MQTT:         mqtt.SplitClient(breaker, mqttClient),
		Logger:       logger,
		Store:        store,
		NewHARuntime: newHARuntime,
		StatePlane:   statePlane,
		Instance:     instance,
		BrokerMaxPacketSize: func() (uint32, bool) {
			res, ok := mqttClient.ConnectResult()
			if !ok {
				return 0, false
			}
			return res.MaximumPacketSize, true
		},
	})

	// Registered before Start so the first connect announces too.
	mqttLifecycle.OnConnect(c.PublishOnline)
	if err := startMQTT(ctx, mqttLifecycle, time.Second, 30*time.Second, logger); err != nil {
		return fmt.Errorf("keba2mqtt: mqtt start: %w", err)
	}
	defer func() {
		// Bounded, so a hung broker cannot block shutdown. A clean
		// DISCONNECT suppresses the will, so say 0 ourselves first.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		c.PublishOffline(stopCtx)
		_ = mqttLifecycle.Stop(stopCtx)
	}()

	go func() { _ = instance.RunStats(ctx) }()

	if !cfg.WebEnable {
		return c.Run(ctx)
	}
	webSrv := web.New(web.Config{
		Bind:     cfg.WebBind,
		User:     cfg.WebUser,
		Password: cfg.WebPassword,
		Logger:   logger,
	}, c)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.Run(gctx) })
	g.Go(func() error { return webSrv.Run(gctx) })
	return g.Wait()
}

// instanceInfo is this project's own `<name>/info` fields.
func instanceInfo(cfg *config.Config) map[string]any {
	return map[string]any{
		"modbus":       net.JoinHostPort(cfg.ModbusIP, strconv.Itoa(cfg.ModbusPort)),
		"ha_discovery": cfg.HASSEnable,
		"evcc":         cfg.EVCCEnable,
	}
}

// mqttStarter is the subset of [*mqtt.Lifecycle] startMQTT drives.
type mqttStarter interface {
	Start(ctx context.Context) error
}

// startMQTT retries the first broker connect with bounded backoff: a
// broker still booting alongside this daemon (power-outage recovery) is
// not a configuration error. Lifecycle.Start makes one attempt and only
// reconnects after a first success.
func startMQTT(ctx context.Context, lc mqttStarter, backoff, maxBackoff time.Duration, logger *slog.Logger) error {
	for {
		err := lc.Start(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("keba2mqtt.mqtt_start_retry",
			slog.String("err", err.Error()),
			slog.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// runHealthcheck implements the container HEALTHCHECK hook: exit 0 when
// healthy or when there is no signal (web UI off, config unreadable),
// exit 1 when the daemon should be serving but does not answer.
func runHealthcheck(configPath string) int {
	cfg, err := loadConfig(configPath, slog.New(slog.DiscardHandler))
	if err != nil || !cfg.WebEnable {
		return 0
	}
	host, port, err := net.SplitHostPort(cfg.WebBind)
	if err != nil {
		return 0
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+net.JoinHostPort(host, port)+"/api/health", http.NoBody)
	if err != nil {
		return 1
	}
	if cfg.WebUser != "" && cfg.WebPassword != "" {
		req.SetBasicAuth(cfg.WebUser, cfg.WebPassword)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}

// loadConfig finds and parses the config. Without a file the config is
// built from KEBA_* environment variables and defaults alone — the Home
// Assistant add-on and env-only `docker run` ship no file.
func loadConfig(explicit string, logger *slog.Logger) (*config.Config, error) {
	env := config.OSEnv{}
	path := explicit
	if path == "" {
		if located, ok := config.Locate(env); ok {
			path = located
		}
	}
	if path == "" {
		cfg, err := config.Load(strings.NewReader(""), env)
		if err != nil {
			return nil, err
		}
		logger.Info("keba2mqtt.config_loaded", slog.String("path", "(environment only)"))
		return cfg, nil
	}
	logger.Info("keba2mqtt.config_loaded", slog.String("path", path))
	return config.LoadFile(path, env)
}

var errTransportNotWired = errors.New("keba2mqtt: transport used before the client was wired")

// deferredTransport is a [publisher.Transport] whose client is supplied
// after construction: the will has to be read from the runtime before
// the MQTT client that carries it exists.
type deferredTransport struct {
	mu sync.RWMutex
	tr publisher.Transport
}

func (d *deferredTransport) wire(tr publisher.Transport) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tr = tr
}

func (d *deferredTransport) target() (publisher.Transport, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.tr == nil {
		return nil, errTransportNotWired
	}
	return d.tr, nil
}

// Publish implements [publisher.Transport].
func (d *deferredTransport) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Publish(ctx, topic, payload, qos, retain)
}

// Subscribe implements [publisher.Transport].
func (d *deferredTransport) Subscribe(ctx context.Context, filter string, qos byte, h publisher.Handler) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Subscribe(ctx, filter, qos, h)
}

// Unsubscribe implements [publisher.Transport].
func (d *deferredTransport) Unsubscribe(ctx context.Context, filter string) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Unsubscribe(ctx, filter)
}
