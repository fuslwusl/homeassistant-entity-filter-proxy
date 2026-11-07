package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// largeFrameLogBytes is the size from which HA→client WebSocket frames and
// HTTP responses are logged together with the request that caused them.
// 0 disables the diagnostics. Set from the config in main.
var largeFrameLogBytes = 1 << 20

const (
	// maxTrackedRequests bounds the per-session request map; subscriptions
	// keep their IDs alive, so entries are not removed on the first result.
	maxTrackedRequests = 4096
	// maxLoggedRequestLen truncates logged request payloads.
	maxLoggedRequestLen = 300
	// frameHeadLen is how much of a frame is scanned for its id and type.
	frameHeadLen = 512
)

// requestTracker remembers client→HA WebSocket requests by message ID so
// that large responses can be attributed to the command that caused them.
type requestTracker struct {
	mu   sync.Mutex
	byID map[int64]string
}

func newRequestTracker() *requestTracker {
	return &requestTracker{byID: make(map[int64]string)}
}

// track records a client request. Messages without an ID (e.g. "auth",
// which carries the access token) are never stored.
func (rt *requestTracker) track(data []byte) {
	if largeFrameLogBytes <= 0 {
		return
	}
	var msg struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &msg) != nil || msg.ID == 0 || msg.Type == "auth" {
		return
	}

	summary := string(data)
	if len(summary) > maxLoggedRequestLen {
		summary = summary[:maxLoggedRequestLen] + "…"
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.byID) >= maxTrackedRequests {
		rt.byID = make(map[int64]string)
	}
	rt.byID[msg.ID] = summary
}

func (rt *requestTracker) lookup(id int64) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if s, ok := rt.byID[id]; ok {
		return s
	}
	return "<unknown>"
}

var (
	frameIDRe   = regexp.MustCompile(`"id"\s*:\s*(\d+)`)
	frameTypeRe = regexp.MustCompile(`"type"\s*:\s*"([^"]*)"`)
)

// peekIDAndType extracts the id and type of the first message in a frame by
// scanning only its first bytes, so large frames are never fully decoded.
func peekIDAndType(data []byte) (int64, string) {
	head := data
	if len(head) > frameHeadLen {
		head = head[:frameHeadLen]
	}
	var id int64
	if m := frameIDRe.FindSubmatch(head); m != nil {
		id, _ = strconv.ParseInt(string(m[1]), 10, 64)
	}
	var typ string
	if m := frameTypeRe.FindSubmatch(head); m != nil {
		typ = string(m[1])
	}
	return id, typ
}

// logLargeFrame logs a large HA→client frame with the request that caused it.
func logLargeFrame(rt *requestTracker, remoteAddr string, data []byte) {
	id, typ := peekIDAndType(data)
	log.Printf("large frame from HA: %.1f MB to client %s, id=%d type=%s, request: %s",
		float64(len(data))/(1<<20), remoteAddr, id, typ, rt.lookup(id))
}

// countingBody wraps an HTTP response body and logs the response once it is
// closed if it was at least largeFrameLogBytes large.
type countingBody struct {
	io.ReadCloser
	n      int64
	method string
	target string
	once   sync.Once
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	b.once.Do(func() {
		if b.n >= int64(largeFrameLogBytes) {
			log.Printf("large HTTP response from HA: %.1f MB for %s %s", float64(b.n)/(1<<20), b.method, b.target)
		}
	})
	return b.ReadCloser.Close()
}

// wrapLargeResponseLogging installs a countingBody on resp if diagnostics
// are enabled.
func wrapLargeResponseLogging(resp *http.Response) {
	if largeFrameLogBytes <= 0 || resp.Body == nil || resp.Request == nil {
		return
	}
	resp.Body = &countingBody{
		ReadCloser: resp.Body,
		method:     resp.Request.Method,
		target:     redactedTarget(resp.Request.URL),
	}
}

// redactedTarget returns path and query with credential-like parameters
// (token, auth, sig, key, password) masked.
func redactedTarget(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		for _, s := range []string{"token", "auth", "sig", "key", "password"} {
			if strings.Contains(lk, s) {
				q.Set(k, "***")
				break
			}
		}
	}
	return u.Path + "?" + q.Encode()
}
