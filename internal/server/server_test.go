package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// counting returns a handler that renders "render N" and counts renders.
func counting(calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "render %d", n)
	})
}

func get(h http.Handler, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newClocked(next http.Handler, ttl time.Duration) (*CachingHandler, *time.Time) {
	h := NewCachingHandler(next, ttl)
	clock := time.Unix(1_000_000, 0)
	h.now = func() time.Time { return clock }
	return h, &clock
}

func TestServesFromCacheWithinTTL(t *testing.T) {
	var calls atomic.Int32
	h, clock := newClocked(counting(&calls), 2*time.Minute)

	first := get(h, nil)
	assert.Equal(t, "render 1", first.Body.String())
	assert.Equal(t, "MISS", first.Header().Get("X-Cache"))
	assert.Equal(t, "text/plain; version=0.0.4", first.Header().Get("Content-Type"))

	// Prometheus servers in other zones scrape a few seconds apart.
	*clock = clock.Add(7 * time.Second)
	second := get(h, nil)
	assert.Equal(t, "render 1", second.Body.String(), "stale copy is served")
	assert.Equal(t, "HIT", second.Header().Get("X-Cache"))
	assert.Equal(t, "7", second.Header().Get("Age"))
	assert.Equal(t, "text/plain; version=0.0.4", second.Header().Get("Content-Type"))

	*clock = clock.Add(100 * time.Second)
	assert.Equal(t, "render 1", get(h, nil).Body.String(), "still inside the 2 minute window")
	assert.EqualValues(t, 1, calls.Load())
}

func TestRerendersAfterTTL(t *testing.T) {
	var calls atomic.Int32
	h, clock := newClocked(counting(&calls), time.Minute)

	get(h, nil)
	*clock = clock.Add(61 * time.Second)
	rec := get(h, nil)

	assert.Equal(t, "render 2", rec.Body.String())
	assert.Equal(t, "MISS", rec.Header().Get("X-Cache"))
	assert.Equal(t, "0", rec.Header().Get("Age"))
}

func TestInvalidateForcesRerender(t *testing.T) {
	var calls atomic.Int32
	h, _ := newClocked(counting(&calls), time.Minute)

	get(h, nil)
	h.Invalidate()
	assert.Equal(t, "render 2", get(h, nil).Body.String())
}

func TestZeroTTLDisablesCache(t *testing.T) {
	var calls atomic.Int32
	h := NewCachingHandler(counting(&calls), 0)

	get(h, nil)
	rec := get(h, nil)
	assert.Equal(t, "render 2", rec.Body.String())
	assert.Empty(t, rec.Header().Get("X-Cache"))
}

func TestCacheIsKeyedByNegotiatedFormat(t *testing.T) {
	var calls atomic.Int32
	h, _ := newClocked(counting(&calls), time.Minute)

	plain := get(h, nil)
	gzipped := get(h, map[string]string{"Accept-Encoding": "gzip"})
	openmetrics := get(h, map[string]string{"Accept": "application/openmetrics-text"})

	assert.Equal(t, "render 1", plain.Body.String())
	assert.Equal(t, "render 2", gzipped.Body.String(), "a gzip client must not receive the plain entry")
	assert.Equal(t, "render 3", openmetrics.Body.String())
	assert.Equal(t, "HIT", get(h, map[string]string{"Accept-Encoding": "gzip"}).Header().Get("X-Cache"))
}

func TestErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	h, _ := newClocked(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "ok")
	}), time.Minute)

	first := get(h, nil)
	assert.Equal(t, http.StatusInternalServerError, first.Code)

	second := get(h, nil)
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, "ok", second.Body.String())
}

func TestCacheSizeIsBounded(t *testing.T) {
	var calls atomic.Int32
	h, _ := newClocked(counting(&calls), time.Minute)

	for i := range maxCacheEntries + 5 {
		get(h, map[string]string{"Accept": fmt.Sprintf("x/%d", i)})
	}
	assert.Len(t, h.entries, maxCacheEntries)

	// Beyond the limit requests still work, just uncached.
	rec := get(h, map[string]string{"Accept": "x/overflow"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, h.entries, maxCacheEntries)
}

func TestConcurrentScrapesShareOneRender(t *testing.T) {
	var calls atomic.Int32
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "body")
	})
	h := NewCachingHandler(slow, time.Minute)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Equal(t, "body", get(h, nil).Body.String())
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())
}

func TestHeadRequestHasNoBody(t *testing.T) {
	var calls atomic.Int32
	h, _ := newClocked(counting(&calls), time.Minute)
	get(h, nil)

	req := httptest.NewRequest(http.MethodHead, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "HIT", rec.Header().Get("X-Cache"))
	assert.Empty(t, rec.Body.String())
}

func TestHealthEndpoints(t *testing.T) {
	ready := false
	mux, _ := New(http.NotFoundHandler(), func() bool { return ready }, time.Minute)

	code := func(path string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, code("/healthz"))
	assert.Equal(t, http.StatusServiceUnavailable, code("/readyz"))
	ready = true
	assert.Equal(t, http.StatusOK, code("/readyz"))
	require.Equal(t, http.StatusNotFound, code("/metrics"), "the wrapped handler is mounted at /metrics")
}
