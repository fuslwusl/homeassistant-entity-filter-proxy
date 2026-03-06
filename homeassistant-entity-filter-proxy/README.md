# Home Assistant Entity Filter Proxy Add-on

This add-on packages the `ha-ws-proxy` application so it can be configured from the Home Assistant UI.

## What It Does

- Reverse proxies Home Assistant HTTP and WebSocket traffic
- Injects `entity_ids` into `subscribe_entities` requests
- Filters `state_changed` events to relevant entities
- Can throttle high-frequency websocket entity updates via `state_update_interval`

## Configuration

All settings are available in the add-on UI:

- `homeassistant_url`: URL of your HA instance (for add-on network use `http://homeassistant:8123`)
- `access_token`: Long-lived access token
- `listen_addr`: Bind address for proxy (default `:8124`)
- `dashboard_url_path`: Dashboard path, empty for default
- `include_all_entities`: Disable filtering if set to `true`
- `state_update_interval`: Update batching interval (for example `3s`, `500ms`)
- `transparent`: Strip `X-Forwarded-*` headers
- `extra_entities`: Extra entity IDs to include
- `include_entity_globs`: Include filter globs
- `exclude_entity_globs`: Exclude filter globs

## Usage

1. Add this repository as a custom add-on repository in Home Assistant.
2. Install `Home Assistant Entity Filter Proxy`.
3. Set add-on options and start it.
4. Point tablet/browser clients to `http://<ha-host>:8124`.
