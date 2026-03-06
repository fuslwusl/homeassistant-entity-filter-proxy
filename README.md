# ha-ws-proxy

A transparent reverse proxy for Home Assistant that filters WebSocket entity subscriptions. Designed for wall-mounted tablets and other low-power devices that only display a single dashboard and don't need state updates for every entity in the system.

## Problem

The HA web frontend subscribes to state changes for **all** entities via `subscribe_entities` with no filter. On systems with hundreds of entities, this creates unnecessary load on low-power clients. The HA backend supports an `entity_ids` parameter (the Android companion app uses it), but the frontend doesn't expose it, and patching the frontend requires maintaining a fork.

## How it works

```
Browser/Tablet ──► ha-ws-proxy (:8124) ──► Home Assistant (:8123)
                   │
                   ├── HTTP requests: reverse proxied unchanged
                   └── WebSocket /api/websocket:
                       ├── subscribe_entities → injects entity_ids filter
                       └── state_changed events → drops non-dashboard entities
```

On startup, the proxy:

1. Connects to HA via WebSocket and authenticates with a long-lived access token
2. Fetches the Lovelace dashboard config (`lovelace/config`)
3. Extracts all entity IDs referenced in the dashboard (cards, badges, actions, etc.)
4. Merges any extra entities from the config file
5. Starts listening as a reverse proxy

At runtime, for each WebSocket connection on `/api/websocket`:

- **Client → HA**: `subscribe_entities` messages get `entity_ids` injected. `subscribe_events` for `state_changed` are tracked by subscription ID.
- **HA → Client**: `state_changed` events for entities not on the dashboard are dropped. Supports HA's coalesced message format (multiple messages batched into a JSON array in a single WebSocket frame).
- All other messages (auth, service calls, other subscriptions) pass through unmodified.
- All other WebSocket paths pass through unmodified.
- All HTTP requests are reverse proxied unchanged.

## Setup

### Prerequisites

- Go 1.21+
- A Home Assistant long-lived access token (create one at `http://your-ha:8123/profile` → Long-Lived Access Tokens)

### Build

```sh
go build -o ha-ws-proxy
```

### Configure

Copy the example config and edit it:

```sh
cp config.example.yaml config.yaml
```

```yaml
# Required
homeassistant_url: "http://homeassistant.local:8123"
access_token: "your-long-lived-access-token-here"

# Optional
listen_addr: ":8124"          # default :8124
dashboard_url_path: ""        # "" = default dashboard, "my-dashboard" for a named one
include_all_entities: false    # true = disable entity filtering and pass all entities through
state_update_interval: "2s"   # optional: batch state_changed updates every 2s
transparent: true             # strip proxy headers (see below)
extra_entities:               # entities to include beyond auto-detected ones
  - sensor.cpu_temperature
  - weather.home
include_entity_globs:         # keep only entities matching these globs
  - light.*
exclude_entity_globs:         # remove entities matching these globs
  - sensor.*_battery
```

### Run

```sh
./ha-ws-proxy -config config.yaml
```

Point your tablet browser to `http://<proxy-host>:8124` instead of directly to HA.

## Transparent mode

By default, Go's reverse proxy adds `X-Forwarded-For` and similar headers. HA will reject these with HTTP 400 unless the proxy's IP is in `trusted_proxies`.

With `transparent: true`, the proxy strips all `X-Forwarded-*` headers so HA sees requests as direct client connections. No `trusted_proxies` configuration needed.

Set `transparent: false` if you want standard proxy behavior and have configured `trusted_proxies` in HA's `http` integration.

## Entity auto-detection

The proxy walks the Lovelace dashboard config and extracts entity IDs from:

- `entity` fields on cards
- `entities` arrays
- `camera_image` fields
- `tap_action` / `hold_action` → `target.entity_id`, `data.entity_id`, `service_data.entity_id`
- Nested structures: `card`, `cards`, `elements`, `badges`, `sections`

This mirrors the frontend's own `computeUsedEntities()` logic.

Entities referenced in templates, custom cards with dynamic entity access, or conditional logic may not be detected. Use `extra_entities` in the config to include these manually.

You can additionally constrain the final list with glob filters:

- `include_entity_globs`: If set, only entities matching at least one pattern are kept.
- `exclude_entity_globs`: Entities matching any pattern are removed.
- Filter order is include first, then exclude.

Glob syntax uses Go's `path.Match` rules (`*`, `?`, and character classes like `[ab]`).

If you want to bypass filtering and include everything, set `include_all_entities: true`.
In that mode, the proxy skips dashboard entity extraction and forwards `/api/websocket` traffic unchanged.
`extra_entities`, `include_entity_globs`, and `exclude_entity_globs` are ignored.

You can also throttle entity updates with `state_update_interval` (Go duration format, e.g. `500ms`, `2s`, `10s`).
When set, `state_changed` events are buffered and flushed once per interval.
For each entity, only the latest pending update in that interval is sent.

**Note**: Strategy-based dashboards cannot be statically analyzed. The proxy will exit with an error if it detects a strategy config. Use a non-strategy dashboard or specify all entities via `extra_entities`.

## Verification

1. Check the startup log — it lists all detected entity IDs
2. Open the dashboard through the proxy
3. In browser DevTools → Network → WS, inspect the `subscribe_entities` frame sent to HA and confirm it includes `entity_ids`
4. Confirm dashboard entities update in real time
5. Confirm `state_changed` events only arrive for dashboard entities

## Limitations

- Entity list is determined at proxy startup. If you change the dashboard, restart the proxy.
- Strategy dashboards are not supported for auto-detection.
- Admin pages (entity browser, developer tools) will only show live state for filtered entities. Use HA directly for admin tasks.
