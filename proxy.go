package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// entityFilter tracks state_changed subscription IDs and provides filtering
// for both client→HA and HA→client directions.
type entityFilter struct {
	allowed map[string]struct{}
	// subscription IDs that are subscribe_events with event_type: state_changed
	stateChangedSubs map[float64]struct{}
	mu               sync.RWMutex
}

func newEntityFilter(entityIDs []string) *entityFilter {
	allowed := make(map[string]struct{}, len(entityIDs))
	for _, id := range entityIDs {
		allowed[id] = struct{}{}
	}
	return &entityFilter{
		allowed:          allowed,
		stateChangedSubs: make(map[float64]struct{}),
	}
}

// wsProxy proxies a WebSocket connection to Home Assistant.
// If filterEntities is true, it injects entity_ids into subscribe_entities messages
// and filters state_changed events to only include allowed entities.
// If false, all messages pass through unmodified (used for non-/api/websocket WS paths).
func wsProxy(haWSURL string, entityIDs []string, filterEntities bool, w http.ResponseWriter, r *http.Request) {
	wsProxyWithInterval(haWSURL, entityIDs, filterEntities, 0, w, r)
}

func wsProxyWithInterval(haWSURL string, entityIDs []string, filterEntities bool, stateUpdateEvery time.Duration, w http.ResponseWriter, r *http.Request) {
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("client websocket upgrade failed: %v", err)
		return
	}

	dialURL := haWSURL + r.URL.Path
	haConn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		log.Printf("connecting to HA websocket at %s failed: %v", dialURL, err)
		clientConn.Close()
		return
	}

	log.Printf("websocket proxy session: %s (filter=%v, state_update_interval=%s)", r.URL.Path, filterEntities, stateUpdateEvery)

	var filter *entityFilter
	if filterEntities {
		filter = newEntityFilter(entityIDs)
	}

	var throttler *stateUpdateThrottler
	if stateUpdateEvery > 0 {
		throttler = newStateUpdateThrottler(stateUpdateEvery)
	}

	var closeOnce sync.Once
	done := make(chan struct{})
	var clientWriteMu sync.Mutex
	writeToClient := func(msgType int, payload []byte) error {
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		return clientConn.WriteMessage(msgType, payload)
	}

	cleanup := func() {
		close(done)
		clientConn.Close()
		haConn.Close()
	}

	if throttler != nil {
		go func() {
			ticker := time.NewTicker(throttler.interval)
			defer ticker.Stop()

			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					payload := throttler.flushPayload()
					if payload == nil {
						continue
					}
					if err := writeToClient(websocket.TextMessage, payload); err != nil {
						if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
							log.Printf("state update flush write error: %v", err)
						}
						return
					}
				}
			}
		}()
	}

	// HA → Client: filter state_changed events for non-allowed entities
	go func() {
		defer closeOnce.Do(cleanup)
		for {
			msgType, data, err := haConn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Printf("ha→client read error: %v", err)
				}
				return
			}

			if filter != nil && msgType == websocket.TextMessage {
				data = filter.filterResponse(data)
				if data == nil {
					continue // all messages in frame were filtered out
				}
			}

			if throttler != nil && msgType == websocket.TextMessage {
				data = throttler.processFrame(data)
				if data == nil {
					continue // state_changed updates buffered for interval flush
				}
			}

			if err := writeToClient(msgType, data); err != nil {
				log.Printf("ha→client write error: %v", err)
				return
			}
		}
	}()

	// Client → HA: inject entity_ids filter and track state_changed subscriptions
	go func() {
		defer closeOnce.Do(cleanup)
		for {
			msgType, data, err := clientConn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Printf("client→ha read error: %v", err)
				}
				return
			}

			if filter != nil && msgType == websocket.TextMessage {
				data = filter.processOutgoing(data, entityIDs)
			}

			if err := haConn.WriteMessage(msgType, data); err != nil {
				log.Printf("client→ha write error: %v", err)
				return
			}
		}
	}()
}

// processOutgoing handles client→HA messages: injects entity_ids into
// subscribe_entities and tracks subscribe_events for state_changed.
func (f *entityFilter) processOutgoing(data []byte, entityIDs []string) []byte {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return data
	}

	var msgType string
	if err := json.Unmarshal(msg["type"], &msgType); err != nil {
		return data
	}

	switch msgType {
	case "subscribe_entities":
		// Inject entity_ids if not already present
		if _, hasFilter := msg["entity_ids"]; !hasFilter {
			entityIDsJSON, err := json.Marshal(entityIDs)
			if err != nil {
				return data
			}
			msg["entity_ids"] = json.RawMessage(entityIDsJSON)
			modified, err := json.Marshal(msg)
			if err != nil {
				return data
			}
			log.Printf("injected entity_ids filter (%d entities) into subscribe_entities", len(entityIDs))
			return modified
		}

	case "subscribe_events":
		// Track state_changed subscriptions so we can filter their responses
		var eventType string
		if raw, ok := msg["event_type"]; ok {
			json.Unmarshal(raw, &eventType)
		}
		if eventType == "state_changed" {
			var id float64
			if raw, ok := msg["id"]; ok {
				json.Unmarshal(raw, &id)
			}
			if id > 0 {
				f.mu.Lock()
				f.stateChangedSubs[id] = struct{}{}
				f.mu.Unlock()
				log.Printf("tracking state_changed subscription id=%.0f for filtering", id)
			}
		}

	case "unsubscribe_events":
		// Stop tracking if the subscription is unsubscribed
		var subID float64
		if raw, ok := msg["subscription"]; ok {
			json.Unmarshal(raw, &subID)
		}
		if subID > 0 {
			f.mu.Lock()
			delete(f.stateChangedSubs, subID)
			f.mu.Unlock()
		}
	}

	return data
}

// filterResponse handles HA→client messages, supporting both single JSON objects
// and coalesced arrays (HA batches multiple messages into one WebSocket frame).
// Returns nil if the entire frame should be dropped, or the (possibly modified) data.
func (f *entityFilter) filterResponse(data []byte) []byte {
	// Detect if this is a JSON array (coalesced messages) or single object
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return data
	}

	if trimmed[0] == '[' {
		return f.filterCoalesced(data)
	}
	if f.shouldDropMessage(data) {
		return nil
	}
	return data
}

// filterCoalesced filters a JSON array of coalesced messages, removing
// state_changed events for non-allowed entities.
func (f *entityFilter) filterCoalesced(data []byte) []byte {
	var messages []json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return data
	}

	kept := messages[:0] // reuse backing array
	for _, msg := range messages {
		if !f.shouldDropMessage(msg) {
			kept = append(kept, msg)
		}
	}

	if len(kept) == len(messages) {
		return data // nothing filtered, return original
	}
	if len(kept) == 0 {
		return nil // all messages filtered out
	}

	result, err := json.Marshal(kept)
	if err != nil {
		return data
	}
	return result
}

// shouldDropMessage checks if a single HA→client message is a state_changed event
// for an entity not in the allowed set.
func (f *entityFilter) shouldDropMessage(data []byte) bool {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return false
	}

	// Only filter "event" type messages
	var msgType string
	if err := json.Unmarshal(msg["type"], &msgType); err != nil || msgType != "event" {
		return false
	}

	// Check if this event belongs to a tracked state_changed subscription
	var id float64
	if err := json.Unmarshal(msg["id"], &id); err != nil {
		return false
	}

	f.mu.RLock()
	_, isTracked := f.stateChangedSubs[id]
	f.mu.RUnlock()
	if !isTracked {
		return false
	}

	// Extract entity_id from event.data.entity_id
	var event struct {
		Data struct {
			EntityID string `json:"entity_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msg["event"], &event); err != nil {
		return false
	}

	_, allowed := f.allowed[event.Data.EntityID]
	return !allowed
}
