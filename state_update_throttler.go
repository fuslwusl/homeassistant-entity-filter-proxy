package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type stateUpdateThrottler struct {
	interval        time.Duration
	mu              sync.Mutex
	pendingByEntity map[string]json.RawMessage
	pendingOther    []json.RawMessage
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

	if trimmed[0] == '[' {
		var messages []json.RawMessage
		if err := json.Unmarshal(data, &messages); err != nil {
			return data
		}

		immediate := make([]json.RawMessage, 0, len(messages))
		for _, msg := range messages {
			if !t.bufferIfStateChanged(msg) {
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

	if t.bufferIfStateChanged(data) {
		return nil
	}

	return data
}

func (t *stateUpdateThrottler) bufferIfStateChanged(raw []byte) bool {
	isStateChanged, entityID := parseStateChangedEntityID(raw)
	if !isStateChanged {
		return false
	}

	msgCopy := append([]byte(nil), raw...)
	msg := json.RawMessage(msgCopy)

	t.mu.Lock()
	defer t.mu.Unlock()

	if entityID == "" {
		t.pendingOther = append(t.pendingOther, msg)
		return true
	}

	t.pendingByEntity[entityID] = msg
	return true
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

	if len(batch) == 1 {
		return []byte(batch[0])
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		return nil
	}

	return payload
}

func parseStateChangedEntityID(raw []byte) (bool, string) {
	var msg struct {
		Type  string `json:"type"`
		Event struct {
			EventType string `json:"event_type"`
			Data      struct {
				EntityID string `json:"entity_id"`
			} `json:"data"`
		} `json:"event"`
	}

	if err := json.Unmarshal(raw, &msg); err != nil {
		return false, ""
	}
	if msg.Type != "event" || msg.Event.EventType != "state_changed" {
		return false, ""
	}

	return true, msg.Event.Data.EntityID
}
