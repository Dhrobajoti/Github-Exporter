package exporter

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github-exporter/internal/ghclient"
	"github-exporter/internal/scanners"
)

// fakeScanner serves canned per-repo results that a test can change between
// refreshes.
type fakeScanner struct {
	mu      sync.Mutex
	results map[string]result // by repo
	active  atomic.Int32
	peak    atomic.Int32
}

type result struct {
	samples []scanners.Sample
	err     error
}

func (f *fakeScanner) Name() string { return "fake" }
func (f *fakeScanner) Metric() scanners.Metric {
	return scanners.Metric{Name: "github_fake_alerts", Help: "fake", Labels: []string{"severity"}}
}

func (f *fakeScanner) set(repo string, r result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[repo] = r
}

func (f *fakeScanner) Scan(_ context.Context, _, repo string) ([]scanners.Sample, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.results[repo]
	return r.samples, r.err
}

func newFake() *fakeScanner { return &fakeScanner{results: map[string]result{}} }

func sample(severity string, n float64) []scanners.Sample {
	return []scanners.Sample{{Labels: []string{severity}, Count: n}}
}

func setup(t *testing.T, repos *[]string, f *fakeScanner) *Exporter {
	t.Helper()
	return New("acme", func(context.Context) ([]string, error) { return *repos, nil }, []scanners.Scanner{f}, 2)
}

func collect(t *testing.T, e *Exporter, names ...string) string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(e))
	mfs, err := reg.Gather()
	require.NoError(t, err)

	var sb strings.Builder
	for _, mf := range mfs {
		for _, want := range names {
			if mf.GetName() != want {
				continue
			}
			for _, m := range mf.GetMetric() {
				sb.WriteString(want + "{")
				for i, l := range m.GetLabel() {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(l.GetName() + "=" + l.GetValue())
				}
				sb.WriteString("} ")
				value := m.GetCounter().GetValue()
				if g := m.GetGauge(); g != nil {
					value = g.GetValue()
				}
				sb.WriteString(strconv.FormatFloat(value, 'f', -1, 64) + "\n")
			}
		}
	}
	return sb.String()
}

func TestRefreshPublishesMetrics(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	f.set("web", result{samples: sample("low", 1)})
	repos := []string{"api", "web"}
	e := setup(t, &repos, f)

	assert.False(t, e.Ready())
	require.NoError(t, e.Refresh(context.Background()))
	assert.True(t, e.Ready())

	got := collect(t, e, "github_fake_alerts")
	assert.Contains(t, got, "github_fake_alerts{repo=api,severity=high} 3")
	assert.Contains(t, got, "github_fake_alerts{repo=web,severity=low} 1")
}

func TestStaleSeriesDisappear(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	f.set("gone", result{samples: sample("high", 9)})
	repos := []string{"api", "gone"}
	e := setup(t, &repos, f)
	require.NoError(t, e.Refresh(context.Background()))

	// All of api's alerts were fixed and closed alerts are no longer counted,
	// and the "gone" repository was deleted.
	f.set("api", result{})
	repos = []string{"api"}
	require.NoError(t, e.Refresh(context.Background()))

	assert.Empty(t, collect(t, e, "github_fake_alerts"))
}

func TestFailedRepoKeepsPreviousCounts(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	f.set("web", result{samples: sample("low", 1)})
	repos := []string{"api", "web"}
	e := setup(t, &repos, f)
	require.NoError(t, e.Refresh(context.Background()))

	f.set("api", result{err: errors.New("HTTP 502")})
	f.set("web", result{samples: sample("low", 2)})
	require.NoError(t, e.Refresh(context.Background()))

	got := collect(t, e, "github_fake_alerts", "github_exporter_errors_total")
	assert.Contains(t, got, "github_fake_alerts{repo=api,severity=high} 3", "previous value kept")
	assert.Contains(t, got, "github_fake_alerts{repo=web,severity=low} 2", "other repos still update")
	assert.Contains(t, got, "github_exporter_errors_total{scanner=fake} 1")
}

func TestNotEnabledIsNotAnErrorAndDropsData(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	repos := []string{"api"}
	e := setup(t, &repos, f)
	require.NoError(t, e.Refresh(context.Background()))

	f.set("api", result{err: ghclient.ErrNotEnabled})
	require.NoError(t, e.Refresh(context.Background()))

	got := collect(t, e, "github_fake_alerts", "github_exporter_errors_total")
	assert.NotContains(t, got, "github_fake_alerts")
	assert.Contains(t, got, "github_exporter_errors_total{scanner=fake} 0")
}

func TestRepoListFailureKeepsSnapshot(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	fail := false
	e := New("acme", func(context.Context) ([]string, error) {
		if fail {
			return nil, errors.New("list failed")
		}
		return []string{"api"}, nil
	}, []scanners.Scanner{f}, 1)
	require.NoError(t, e.Refresh(context.Background()))

	fail = true
	require.Error(t, e.Refresh(context.Background()))

	got := collect(t, e, "github_fake_alerts", "github_exporter_errors_total")
	assert.Contains(t, got, "github_fake_alerts{repo=api,severity=high} 3")
	assert.Contains(t, got, "github_exporter_errors_total{scanner=repos} 1")
}

func TestCancelledRefreshKeepsSnapshot(t *testing.T) {
	f := newFake()
	f.set("api", result{samples: sample("high", 3)})
	repos := []string{"api"}
	e := setup(t, &repos, f)
	require.NoError(t, e.Refresh(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.set("api", result{samples: sample("high", 99)})
	require.ErrorIs(t, e.Refresh(ctx), context.Canceled)

	assert.Contains(t, collect(t, e, "github_fake_alerts"), "github_fake_alerts{repo=api,severity=high} 3")
}

func TestMetaMetricsOnlyAfterFirstRefresh(t *testing.T) {
	f := newFake()
	repos := []string{"api"}
	e := setup(t, &repos, f)

	names := []string{"github_exporter_last_refresh_timestamp_seconds", "github_exporter_last_refresh_duration_seconds", "github_exporter_repositories"}
	assert.Empty(t, collect(t, e, names...))

	require.NoError(t, e.Refresh(context.Background()))
	got := collect(t, e, names...)
	for _, n := range names {
		assert.Contains(t, got, n)
	}
	assert.Contains(t, got, "github_exporter_repositories{} 1")
}

func TestConcurrencyIsBounded(t *testing.T) {
	f := newFake()
	repos := make([]string, 30)
	for i := range repos {
		repos[i] = "repo-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		f.set(repos[i], result{samples: sample("high", 1)})
	}
	e := New("acme", func(context.Context) ([]string, error) { return repos, nil }, []scanners.Scanner{f}, 3)

	require.NoError(t, e.Refresh(context.Background()))
	assert.LessOrEqual(t, f.peak.Load(), int32(3))
	assert.Equal(t, 30, testutil.CollectAndCount(e, "github_fake_alerts"))
}

func TestOverlappingRefreshIsRejected(t *testing.T) {
	f := newFake()
	repos := []string{"api"}
	e := setup(t, &repos, f)

	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	assert.ErrorIs(t, e.Refresh(context.Background()), ErrRefreshInProgress)
}
