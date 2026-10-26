#!/usr/bin/with-contenv bashio
# SPDX-License-Identifier: MIT
# Home Assistant add-on entrypoint for go-keba2mqtt.
#
# Reads the add-on options (/data/options.json) via bashio, maps them onto
# the daemon's KEBA_* environment variables, wires up Ingress, and exec's
# the binary so it becomes PID 1 and receives signals directly. The add-on
# ships no config file — every setting is supplied through KEBA_* env
# (the daemon falls back to env-only when no config.yaml is found).
#
# Every `bashio::config` read is assigned to a plain variable BEFORE it is
# exported: `export VAR="$(cmd)"` in one step discards the exit status of
# the command substitution (SC2155), so a failing bashio call would
# silently produce an empty value instead of aborting the script.
set -euo pipefail

bashio::log.info "Starting go-keba2mqtt add-on..."

# --- Modbus (KEBA KeContact P40) ---
KEBA_MODBUS_IP="$(bashio::config 'modbus_ip')"
KEBA_MODBUS_PORT="$(bashio::config 'modbus_port')"
KEBA_MODBUS_UNIT_ID="$(bashio::config 'modbus_unit_id')"
KEBA_MODBUS_TIMEOUT="$(bashio::config 'modbus_timeout')"
export KEBA_MODBUS_IP KEBA_MODBUS_PORT KEBA_MODBUS_UNIT_ID KEBA_MODBUS_TIMEOUT

# --- MQTT ---
# Zero-config: with mqtt_server empty, use the broker the Supervisor knows
# (the HA MQTT integration / core-mosquitto) via the mqtt service. An
# explicit mqtt_server always wins; with nothing set and no service
# offered, fall back to core-mosquitto:1883.
if bashio::config.has_value 'mqtt_server'; then
  KEBA_MQTT_SERVER="$(bashio::config 'mqtt_server')"
  KEBA_MQTT_PORT="$(bashio::config 'mqtt_port')"
  KEBA_MQTT_LOGIN="$(bashio::config 'mqtt_login')"
  KEBA_MQTT_PASSWORD="$(bashio::config 'mqtt_password')"
  export KEBA_MQTT_SERVER KEBA_MQTT_PORT KEBA_MQTT_LOGIN KEBA_MQTT_PASSWORD
elif bashio::services.available 'mqtt'; then
  bashio::log.info "mqtt_server empty; using the Home Assistant MQTT service."
  KEBA_MQTT_SERVER="$(bashio::services 'mqtt' 'host')"
  KEBA_MQTT_PORT="$(bashio::services 'mqtt' 'port')"
  KEBA_MQTT_LOGIN="$(bashio::services 'mqtt' 'username')"
  KEBA_MQTT_PASSWORD="$(bashio::services 'mqtt' 'password')"
  export KEBA_MQTT_SERVER KEBA_MQTT_PORT KEBA_MQTT_LOGIN KEBA_MQTT_PASSWORD
else
  bashio::log.warning "mqtt_server empty and no MQTT service offered; falling back to core-mosquitto:1883."
  export KEBA_MQTT_SERVER="core-mosquitto"
  export KEBA_MQTT_PORT="1883"
fi

# --- MQTT TLS ---
# mqtt_tls switches to tls:// for an explicit mqtt_server; the auto-detected
# Home Assistant MQTT service turns it on by itself when it reports ssl.
# mqtt_tls_ca / mqtt_tls_insecure apply to either broker. A CA file belongs in
# /ssl or /share (both mapped read-only). Setting both is refused by the
# daemon's config validation, so it is not second-guessed here.
if bashio::config.true 'mqtt_tls'; then
  export KEBA_MQTT_TLS=true
elif ! bashio::config.has_value 'mqtt_server' \
  && bashio::services.available 'mqtt' \
  && [[ "$(bashio::services 'mqtt' 'ssl')" == "true" ]]; then
  bashio::log.info "The Home Assistant MQTT service reports TLS; connecting via tls://."
  export KEBA_MQTT_TLS=true
fi
if bashio::config.true 'mqtt_tls_insecure'; then
  bashio::log.warning "mqtt_tls_insecure is on: the broker certificate is NOT verified. Prefer mqtt_tls_ca."
  export KEBA_MQTT_TLS_INSECURE=true
fi
if bashio::config.has_value 'mqtt_tls_ca'; then
  KEBA_MQTT_TLS_CA="$(bashio::config 'mqtt_tls_ca')"
  export KEBA_MQTT_TLS_CA
fi

KEBA_MQTT_TOPIC="$(bashio::config 'mqtt_topic')"
export KEBA_MQTT_TOPIC

# --- mqtt-smarthome maintenance topics ---
KEBA_MQTT_MAINTENANCE="$(bashio::config 'mqtt_maintenance')"
KEBA_MQTT_STATS_INTERVAL="$(bashio::config 'mqtt_stats_interval')"
export KEBA_MQTT_MAINTENANCE KEBA_MQTT_STATS_INTERVAL
# A clean exit stops an add-on rather than restarting it, so the MQTT
# restart command would be a stop. Refuse it; restart the add-on from HA.
export KEBA_SUPERVISED=0

# --- Home Assistant discovery ---
KEBA_HASS_ENABLE="$(bashio::config 'hass_enable')"
export KEBA_HASS_ENABLE
if bashio::config.has_value 'device_name'; then
  KEBA_DEVICE_NAME="$(bashio::config 'device_name')"
  export KEBA_DEVICE_NAME
fi

# --- Features ---
KEBA_RFID_ENABLE="$(bashio::config 'rfid_enable')"
KEBA_EVCC_ENABLE="$(bashio::config 'evcc_enable')"
KEBA_FAILSAFE_KEEPALIVE="$(bashio::config 'failsafe_keepalive')"
export KEBA_RFID_ENABLE KEBA_EVCC_ENABLE KEBA_FAILSAFE_KEEPALIVE

# --- Misc ---
KEBA_LANGUAGE="$(bashio::config 'language')"
KEBA_DEBUG="$(bashio::config 'debug')"
export KEBA_LANGUAGE KEBA_DEBUG

# --- Diagnostic web UI / Ingress ---
# Bind to all interfaces on 8080 so the Supervisor's Ingress proxy can reach
# the UI. The SPA uses relative URLs, so it works behind the ingress prefix.
KEBA_WEB_ENABLE="$(bashio::config 'web_enable')"
export KEBA_WEB_ENABLE
export KEBA_WEB_BIND="0.0.0.0:8080"

bashio::log.info "Configuration prepared; MQTT server: ${KEBA_MQTT_SERVER}:${KEBA_MQTT_PORT} (TLS: ${KEBA_MQTT_TLS:-false}), wallbox: ${KEBA_MODBUS_IP}:${KEBA_MODBUS_PORT}"

exec /usr/bin/keba2mqtt
