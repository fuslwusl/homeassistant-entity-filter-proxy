package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// fetchLovelaceConfig connects to HA via WebSocket, authenticates, and fetches
// the Lovelace dashboard configuration.
func fetchLovelaceConfig(haURL, token, dashboardURLPath string) (map[string]any, error) {
	wsURL, err := httpToWS(haURL)
	if err != nil {
		return nil, err
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/api/websocket", nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to HA websocket: %w", err)
	}
	defer conn.Close()

	// Step 1: Read auth_required
	var authRequired map[string]any
	if err := conn.ReadJSON(&authRequired); err != nil {
		return nil, fmt.Errorf("reading auth_required: %w", err)
	}
	if authRequired["type"] != "auth_required" {
		return nil, fmt.Errorf("expected auth_required, got: %v", authRequired["type"])
	}

	// Step 2: Send auth
	authMsg := map[string]any{
		"type":         "auth",
		"access_token": token,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		return nil, fmt.Errorf("sending auth: %w", err)
	}

	// Step 3: Read auth_ok
	var authResp map[string]any
	if err := conn.ReadJSON(&authResp); err != nil {
		return nil, fmt.Errorf("reading auth response: %w", err)
	}
	if authResp["type"] == "auth_invalid" {
		return nil, fmt.Errorf("authentication failed: %v", authResp["message"])
	}
	if authResp["type"] != "auth_ok" {
		return nil, fmt.Errorf("expected auth_ok, got: %v", authResp["type"])
	}

	// Step 4: Fetch Lovelace config
	configReq := map[string]any{
		"id":       1,
		"type":     "lovelace/config",
		"url_path": nil,
		"force":    false,
	}
	if dashboardURLPath != "" {
		configReq["url_path"] = dashboardURLPath
	}
	if err := conn.WriteJSON(configReq); err != nil {
		return nil, fmt.Errorf("sending lovelace/config request: %w", err)
	}

	// Step 5: Read result
	_, rawMsg, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("reading lovelace/config response: %w", err)
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rawMsg, &resp); err != nil {
		return nil, fmt.Errorf("parsing lovelace/config response: %w", err)
	}

	// Check success
	var success bool
	if err := json.Unmarshal(resp["success"], &success); err != nil || !success {
		return nil, fmt.Errorf("lovelace/config request failed: %s", string(rawMsg))
	}

	// Parse the result as a generic map
	var config map[string]any
	if err := json.Unmarshal(resp["result"], &config); err != nil {
		return nil, fmt.Errorf("parsing lovelace config: %w", err)
	}

	return config, nil
}

// httpToWS converts an HTTP(S) URL to a WS(S) URL.
func httpToWS(httpURL string) (string, error) {
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// already correct
	default:
		return "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	// Remove trailing slash
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
