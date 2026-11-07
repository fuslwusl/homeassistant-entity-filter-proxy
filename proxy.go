package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Client connection liveness: a write that blocks longer than clientWriteWait,
// or no pong within clientPongWait, ends the session so that dead or stalled
// clients (sleeping tablets, dropped WiFi) don't keep buffers alive forever.
const (
	clientWriteWait    = 10 * time.Second
	clientPongWait     = 60 * time.Second
	clientPingInterval = 25 * time.Second
)

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
	// Forward auth-relevant headers (Cookie, Authorization) from the client's
	// WebSocket upgrade request so that Ingress and other authenticated WS
	// endpoints (e.g. add-on terminals, live-log viewers) can validate the
	// session.  For /api/websocket auth is handled via the WS auth message, so
	// forwarding these headers there is harmless.
	haDialHeaders := http.Header{}
	for _, name := range []string{"Cookie", "Authorization"} {
		if v := r.Header.Get(name); v != "" {
			haDialHeaders.Set(name, v)
		}
	}
	if len(haDialHeaders) == 0 {
		haDialHeaders = nil
	}
	haConn, _, err := websocket.DefaultDialer.Dial(dialURL, haDialHeaders)
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
		clientConn.SetWriteDeadline(time.Now().Add(clientWriteWait))
		return clientConn.WriteMessage(msgType, payload)
	}

	// Per-session diagnostics: attribute large HA frames to the client
	// request that caused them and log traffic totals when the session ends.
	tracker := newRequestTracker()
	sessionStart := time.Now()
	var haFrames, haBytes atomic.Int64

	cleanup := func() {
		close(done)
		clientConn.Close()
		haConn.Close()
		log.Printf("websocket session closed: %s client %s, duration %s, %d frames / %.1f MB from HA",
			r.URL.Path, r.RemoteAddr, time.Since(sessionStart).Round(time.Second),
			haFrames.Load(), float64(haBytes.Load())/(1<<20))
	}

	clientConn.SetReadDeadline(time.Now().Add(clientPongWait))
	clientConn.SetPongHandler(func(string) error {
		return clientConn.SetReadDeadline(time.Now().Add(clientPongWait))
	})

	go func() {
		defer closeOnce.Do(cleanup)
		ticker := time.NewTicker(clientPingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := clientConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(clientWriteWait)); err != nil {
					log.Printf("client ping failed, closing session: %v", err)
					return
				}
			}
		}
	}()

	if throttler != nil {
		go func() {
			defer closeOnce.Do(cleanup)
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

			haFrames.Add(1)
			haBytes.Add(int64(len(data)))
			if largeFrameLogBytes > 0 && len(data) >= largeFrameLogBytes {
				logLargeFrame(tracker, r.RemoteAddr, data)
			}

			// Cheap substring check first: fully decoding every frame just to
			// spot auth_invalid costs ~12x the frame size in heap and caused
			// OOM kills on large frames (e.g. history streams).
			if msgType == websocket.TextMessage && bytes.Contains(data, []byte(`"auth_invalid"`)) {
				var peek struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}
				if json.Unmarshal(data, &peek) == nil && peek.Type == "auth_invalid" {
					log.Printf("auth_invalid from HA for client %s: %s", r.RemoteAddr, peek.Message)
				}
			}

			if filter != nil && msgType == websocket.TextMessage {
				data = filter.filterResponse(data)
				if data == nil {
					continue // all messages in frame were filtered out
				}
			}

			if throttler != nil && msgType == websocket.TextMessage {
				data = throttler.processFrame(data)
				if throttler.overflowed() {
					log.Printf("client %s is not keeping up with state updates (>%d bytes buffered), closing session", r.RemoteAddr, maxPendingBytes)
					return
				}
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

			if msgType == websocket.TextMessage {
				tracker.track(data)
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
