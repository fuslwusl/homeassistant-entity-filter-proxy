# Changelog

## 0.1.5

### Fixed

- Out-of-memory crash with `state_update_interval` enabled: when a client stopped reading (sleeping tablet, dropped WiFi, stalled browser), throttled state updates were buffered without limit until the host ran out of memory.
  - The buffer is now capped at 8 MB per client; a client exceeding it is disconnected and resyncs on reconnect.
  - Writes to clients time out after 10 s.
  - Clients are pinged every 25 s and disconnected if no pong arrives within 60 s.
  - A failed flush of throttled updates now closes the whole session instead of leaving it half-open.
