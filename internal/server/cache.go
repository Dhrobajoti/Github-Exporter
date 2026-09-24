// Package server contains the HTTP layer of the exporter: the response cache
// in front of /metrics and the routing of the health endpoints.
package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// maxCacheEntries bounds memory: responses are cached per distinct
// Accept / Accept-Encoding / query combination, and a Prometheus fleet only
// ever sends a handful. Requests beyond the limit are served uncached.
const maxCacheEntries = 8

type cacheEntry struct {
	header  http.Header
	body    []byte
	created time.Time
}

// CachingHandler serves a rendered response from memory for up to ttl.
//
// Several Prometheus servers scraping the same exporter then cost one
// rendering per ttl instead of one per scrape. Concurrent requests that arrive
// while a response is being rendered wait for it and share the result.
type CachingHandler struct {
	next http.Handler
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

// NewCachingHandler wraps next. A ttl of zero or less disables caching.
func NewCachingHandler(next http.Handler, ttl time.Duration) *CachingHandler {
	return &CachingHandler{next: next, ttl: ttl, now: time.Now, entries: map[string]*cacheEntry{}}
}

// Invalidate drops every cached response so the next request re-renders.
func (h *CachingHandler) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.entries)
}

func (h *CachingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.ttl <= 0 || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		h.next.ServeHTTP(w, r)
		return
	}
	key := r.Header.Get("Accept") + "\x00" + r.Header.Get("Accept-Encoding") + "\x00" + r.URL.RawQuery

	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.now()
	if e, ok := h.entries[key]; ok && now.Sub(e.created) < h.ttl {
		write(w, r, e, "HIT", now)
		return
	}

	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	h.next.ServeHTTP(rec, r)

	e := &cacheEntry{header: rec.header, body: rec.body, created: now}
	if rec.status == http.StatusOK {
		if _, exists := h.entries[key]; exists || len(h.entries) < maxCacheEntries {
			h.entries[key] = e
		}
	} else {
		delete(h.entries, key)
	}
	writeStatus(w, r, e, rec.status, "MISS", now)
}

func write(w http.ResponseWriter, r *http.Request, e *cacheEntry, state string, now time.Time) {
	writeStatus(w, r, e, http.StatusOK, state, now)
}

func writeStatus(w http.ResponseWriter, r *http.Request, e *cacheEntry, status int, state string, now time.Time) {
	dst := w.Header()
	for k, v := range e.header {
		dst[k] = v
	}
	dst.Set("X-Cache", state)
	dst.Set("Age", strconv.Itoa(int(now.Sub(e.created).Seconds())))
	dst.Set("Content-Length", strconv.Itoa(len(e.body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.body)
	}
}

// recorder captures a handler's response.
type recorder struct {
	header http.Header
	status int
	body   []byte
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int)   { r.status = s }
func (r *recorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return len(b), nil
}
