# Changelog

## 0.1.6

### Fixed

- Out-of-memory kills (0.3–1.5 GB RSS) when clients reconnect and Home Assistant sends large WebSocket frames (e.g. history streams for graph cards).
  - The `auth_invalid` log check no longer fully decodes every frame into a generic map; it only parses frames that actually contain `"auth_invalid"`. This alone cost about 12x the frame size in heap.
  - The state update throttler decodes each message once into a typed struct instead of several times, skips non-event frames without decoding them, and no longer copies buffered messages.
  - A soft memory limit of 256 MB (`debug.SetMemoryLimit`) makes the garbage collector reclaim memory earlier. Override it with the `GOMEMLIMIT` environment variable.

## 0.1.5

### Fixed

- Out-of-memory crash with `state_update_interval` enabled: when a client stopped reading (sleeping tablet, dropped WiFi, stalled browser), throttled state updates were buffered without limit until the host ran out of memory.
  - The buffer is now capped at 8 MB per client; a client exceeding it is disconnected and resyncs on reconnect.
  - Writes to clients time out after 10 s.
  - Clients are pinged every 25 s and disconnected if no pong arrives within 60 s.
  - A failed flush of throttled updates now closes the whole session instead of leaving it half-open.
