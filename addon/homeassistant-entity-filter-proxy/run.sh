#!/bin/sh
set -eu

OPTIONS_FILE="/data/options.json"
APP_CONFIG_FILE="/data/config.yaml"

if [ ! -f "$OPTIONS_FILE" ]; then
  echo "[error] Missing options file at $OPTIONS_FILE"
  exit 1
fi

HOMEASSISTANT_URL=$(jq -ec '.homeassistant_url' "$OPTIONS_FILE")
ACCESS_TOKEN=$(jq -ec '.access_token' "$OPTIONS_FILE")
LISTEN_ADDR=$(jq -ec '.listen_addr // ":8124"' "$OPTIONS_FILE")
DASHBOARD_URL_PATH=$(jq -ec '.dashboard_url_path // ""' "$OPTIONS_FILE")
INCLUDE_ALL_ENTITIES=$(jq -er '.include_all_entities // false' "$OPTIONS_FILE")
STATE_UPDATE_INTERVAL=$(jq -ec '.state_update_interval // ""' "$OPTIONS_FILE")
TRANSPARENT=$(jq -er '.transparent // true' "$OPTIONS_FILE")
EXTRA_ENTITIES=$(jq -ec '.extra_entities // []' "$OPTIONS_FILE")
INCLUDE_ENTITY_GLOBS=$(jq -ec '.include_entity_globs // []' "$OPTIONS_FILE")
EXCLUDE_ENTITY_GLOBS=$(jq -ec '.exclude_entity_globs // []' "$OPTIONS_FILE")

{
  echo "homeassistant_url: ${HOMEASSISTANT_URL}"
  echo "access_token: ${ACCESS_TOKEN}"
  echo "listen_addr: ${LISTEN_ADDR}"
  echo "dashboard_url_path: ${DASHBOARD_URL_PATH}"
  echo "include_all_entities: ${INCLUDE_ALL_ENTITIES}"
  echo "state_update_interval: ${STATE_UPDATE_INTERVAL}"
  echo "transparent: ${TRANSPARENT}"
  echo "extra_entities: ${EXTRA_ENTITIES}"
  echo "include_entity_globs: ${INCLUDE_ENTITY_GLOBS}"
  echo "exclude_entity_globs: ${EXCLUDE_ENTITY_GLOBS}"
} > "$APP_CONFIG_FILE"

echo "[info] Starting ha-ws-proxy"
exec /usr/bin/ha-ws-proxy -config "$APP_CONFIG_FILE"
