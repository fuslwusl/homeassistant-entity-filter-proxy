package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// maxPendingBytes caps the memory buffered between two flushes. If a client
// can't keep up (flush blocked on a slow/dead connection), the buffer would
// otherwise grow without bound because buffering removes the backpressure
// HA would normally see.
const maxPendingBytes = 8 << 20

type stateUpdateThrottler struct {
	interval        time.Duration
	mu              sync.Mutex
	pendingByEntity map[string]json.RawMessage
	pendingOther    []json.RawMessage
	pendingBytes    int
}

func newStateUpdateThrottler(interval time.Duration) *stateUpdateThrottler {
	return &stateUpdateThrottler{
		interval:        interval,
		pendingByEntity: make(map[string]json.RawMessage),
	}
}

// processFrame buffers state_changed events and returns non-state_changed
// messages that should be sent immediately.
func (t *stateUpdateThrottler) processFrame(data []byte) []byte {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return data
	}

	// Only "event" messages can be throttled. Skip everything else (e.g.
	// large get_states results) without decoding it.
	if !bytes.Contains(trimmed, []byte(`"event"`)) {
		return data
	}

	if trimmed[0] == '[' {
		var messages []json.RawMessage
		if err := json.Unmarshal(data, &messages); err != nil {
			return data
		}

		immediate := make([]json.RawMessage, 0, len(messages))
		for _, msg := range messages {
			if !t.bufferIfThrottled(msg) {
				immediate = append(immediate, msg)
			}
		}

		if len(immediate) == 0 {
			return nil
		}
		if len(immediate) == len(messages) {
			return data
		}

		payload, err := json.Marshal(immediate)
		if err != nil {
			return data
		}
		return payload
	}

	if t.bufferIfThrottled(data) {
		return nil
	}

	return data
}

func (t *stateUpdateThrottler) bufferIfThrottled(raw []byte) bool {
	throttle, entityID := classifyEvent(raw)
	if !throttle {
		return false
	}

	// No copy needed: raw is either a frame freshly allocated by
	// ReadMessage or an element decoded from a coalesced array, and the
	// caller does not reuse it once it has been buffered.
	msg := json.RawMessage(raw)

	t.mu.Lock()
	defer t.mu.Unlock()

	if entityID == "" {
		t.pendingOther = append(t.pendingOther, msg)
		t.pendingBytes += len(msg)
		return true
	}

	t.pendingBytes += len(msg) - len(t.pendingByEntity[entityID])
	t.pendingByEntity[entityID] = msg
	return true
}

// overflowed reports whether the buffered updates exceed maxPendingBytes.
func (t *stateUpdateThrottler) overflowed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingBytes > maxPendingBytes
}

func (t *stateUpdateThrottler) flushPayload() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.pendingByEntity) == 0 && len(t.pendingOther) == 0 {
		return nil
	}

	keys := make([]string, 0, len(t.pendingByEntity))
	for key := range t.pendingByEntity {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	batch := make([]json.RawMessage, 0, len(keys)+len(t.pendingOther))
	for _, key := range keys {
		batch = append(batch, t.pendingByEntity[key])
	}
	batch = append(batch, t.pendingOther...)

	t.pendingByEntity = make(map[string]json.RawMessage)
	t.pendingOther = nil
	t.pendingBytes = 0

	if len(batch) == 1 {
		return []byte(batch[0])
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		return nil
	}

	return payload
}

// presence records that a JSON field exists without allocating a copy of
// its (possibly large) value.
type presence bool

func (p *presence) UnmarshalJSON([]byte) error {
	*p = true
	return nil
}

// classifyEvent decodes a single message once and reports whether it is a
// throttleable event: a state_changed event (entityID set when present) or a
// subscribe_entities update (keys "a", "c" or "r"; entityID empty).
func classifyEvent(raw []byte) (throttle bool, entityID string) {
	var msg struct {
		Type  string `json:"type"`
		Event struct {
			EventType string `json:"event_type"`
			Data      struct {
				EntityID string `json:"entity_id"`
			} `json:"data"`
			Added   presence `json:"a"`
			Changed presence `json:"c"`
			Removed presence `json:"r"`
		} `json:"event"`
	}

	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "event" {
		return false, ""
	}
	if msg.Event.EventType == "state_changed" {
		return true, msg.Event.Data.EntityID
	}
	if msg.Event.Added || msg.Event.Changed || msg.Event.Removed {
		return true, ""
	}
	return false, ""
}
