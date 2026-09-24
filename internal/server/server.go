package server

import (
	"net/http"
	"time"
)

// New returns the exporter's HTTP handler.
//
//	/metrics  the given handler, cached for ttl (see CachingHandler)
//	/healthz  200 while the process is up
//	/readyz   200 once ready() reports true, 503 before
//
// The returned CachingHandler lets the caller invalidate the /metrics cache.
func New(metrics http.Handler, ready func() bool, ttl time.Duration) (http.Handler, *CachingHandler) {
	cache := NewCachingHandler(metrics, ttl)

	mux := http.NewServeMux()
	mux.Handle("/metrics", cache)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready() {
			http.Error(w, "no refresh has completed yet", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux, cache
}
