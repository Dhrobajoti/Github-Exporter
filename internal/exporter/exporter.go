// Package exporter refreshes GitHub alert counts and serves them as Prometheus
// metrics.
//
// Counts live in an immutable snapshot that each refresh replaces as a whole.
// That gives two properties a plain GaugeVec does not:
//
//   - a series disappears when its alerts (or its repository) do, instead of
//     staying at its last value forever;
//   - when fetching one repository fails, its previous counts are kept rather
//     than dropped, so a transient API error does not look like "all fixed".
package exporter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"

	"github-exporter/internal/ghclient"
	"github-exporter/internal/scanners"
)

// ErrRefreshInProgress is returned by Refresh when another refresh is running.
var ErrRefreshInProgress = errors.New("a refresh is already in progress")

// scannerLabelRepos is the value of the scanner label on errors that happen
// while listing repositories rather than while scanning one.
const scannerLabelRepos = "repos"

// snapshot maps scanner name -> repo -> samples. It is never modified once
// published.
type snapshot map[string]map[string][]scanners.Sample

// Exporter implements prometheus.Collector.
type Exporter struct {
	listRepos   func(context.Context) ([]string, error)
	org         string
	scanners    []scanners.Scanner
	concurrency int

	descs map[string]*prometheus.Desc // by scanner name

	refreshMu sync.Mutex // held for the duration of a refresh

	mu           sync.RWMutex
	data         snapshot
	lastRefresh  time.Time
	lastDuration time.Duration
	repoCount    int

	errorsTotal *prometheus.CounterVec

	lastRefreshDesc  *prometheus.Desc
	lastDurationDesc *prometheus.Desc
	repoCountDesc    *prometheus.Desc
}

// New returns an Exporter that scans the repositories returned by listRepos
// with every scanner, running at most concurrency scans in parallel.
func New(org string, listRepos func(context.Context) ([]string, error), s []scanners.Scanner, concurrency int) *Exporter {
	e := &Exporter{
		org:         org,
		listRepos:   listRepos,
		scanners:    s,
		concurrency: max(concurrency, 1),
		descs:       map[string]*prometheus.Desc{},
		data:        snapshot{},
		errorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "github_exporter_errors_total",
			Help: "Number of failed GitHub API fetches, by scanner (\"repos\" for repository listing)",
		}, []string{"scanner"}),
		lastRefreshDesc: prometheus.NewDesc("github_exporter_last_refresh_timestamp_seconds",
			"Unix time of the last completed refresh", nil, nil),
		lastDurationDesc: prometheus.NewDesc("github_exporter_last_refresh_duration_seconds",
			"Duration of the last completed refresh", nil, nil),
		repoCountDesc: prometheus.NewDesc("github_exporter_repositories",
			"Number of repositories scanned in the last completed refresh", nil, nil),
	}
	for _, sc := range s {
		m := sc.Metric()
		e.descs[sc.Name()] = prometheus.NewDesc(m.Name, m.Help, append([]string{"repo"}, m.Labels...), nil)
		e.errorsTotal.WithLabelValues(sc.Name()) // export 0 rather than an absent series
	}
	e.errorsTotal.WithLabelValues(scannerLabelRepos)
	return e
}

// Ready reports whether at least one refresh has completed.
func (e *Exporter) Ready() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return !e.lastRefresh.IsZero()
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range e.descs {
		ch <- d
	}
	ch <- e.lastRefreshDesc
	ch <- e.lastDurationDesc
	ch <- e.repoCountDesc
	e.errorsTotal.Describe(ch)
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.RLock()
	data, last, dur, repos := e.data, e.lastRefresh, e.lastDuration, e.repoCount
	e.mu.RUnlock()

	for name, byRepo := range data {
		desc := e.descs[name]
		for repo, samples := range byRepo {
			for _, s := range samples {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, s.Count, append([]string{repo}, s.Labels...)...)
			}
		}
	}
	if !last.IsZero() {
		ch <- prometheus.MustNewConstMetric(e.lastRefreshDesc, prometheus.GaugeValue, float64(last.Unix()))
		ch <- prometheus.MustNewConstMetric(e.lastDurationDesc, prometheus.GaugeValue, dur.Seconds())
		ch <- prometheus.MustNewConstMetric(e.repoCountDesc, prometheus.GaugeValue, float64(repos))
	}
	e.errorsTotal.Collect(ch)
}

type scanResult struct {
	scanner string
	repo    string
	samples []scanners.Sample
	err     error
}

// Refresh scans every repository with every scanner and publishes the result.
//
// It returns an error only if the repository list could not be fetched or the
// context ended; failures of individual repositories are logged, counted in
// github_exporter_errors_total and covered by the previous data.
func (e *Exporter) Refresh(ctx context.Context) error {
	if !e.refreshMu.TryLock() {
		return ErrRefreshInProgress
	}
	defer e.refreshMu.Unlock()

	start := time.Now()
	repos, err := e.listRepos(ctx)
	if err != nil {
		e.errorsTotal.WithLabelValues(scannerLabelRepos).Inc()
		return err
	}
	log.Infof("Scanning %d repositories in %s", len(repos), e.org)

	e.mu.RLock()
	prev := e.data
	e.mu.RUnlock()

	results := e.scanAll(ctx, repos)
	if err := ctx.Err(); err != nil {
		return err // partial results would look like mass fixes; keep the old snapshot
	}

	next := snapshot{}
	for _, sc := range e.scanners {
		next[sc.Name()] = map[string][]scanners.Sample{}
	}
	failed := 0
	for _, r := range results {
		switch {
		case r.err == nil:
			if len(r.samples) > 0 {
				next[r.scanner][r.repo] = r.samples
			}
		case errors.Is(r.err, ghclient.ErrNotEnabled):
			log.Debugf("%s is not enabled for %s/%s", r.scanner, e.org, r.repo)
		default:
			failed++
			e.errorsTotal.WithLabelValues(r.scanner).Inc()
			log.Warnf("%s: failed to fetch alerts for %s/%s: %v", r.scanner, e.org, r.repo, r.err)
			if old, ok := prev[r.scanner][r.repo]; ok {
				next[r.scanner][r.repo] = old
			}
		}
	}

	elapsed := time.Since(start)
	e.mu.Lock()
	e.data = next
	e.lastRefresh = time.Now()
	e.lastDuration = elapsed
	e.repoCount = len(repos)
	e.mu.Unlock()

	log.Infof("Refresh finished in %s (%d failed repository scans)", elapsed.Round(time.Millisecond), failed)
	return nil
}

// scanAll runs every (scanner, repo) pair with bounded parallelism.
func (e *Exporter) scanAll(ctx context.Context, repos []string) []scanResult {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make([]scanResult, 0, len(repos)*len(e.scanners))
		sem     = make(chan struct{}, e.concurrency)
	)

	for _, repo := range repos {
		for _, sc := range e.scanners {
			sem <- struct{}{}
			if ctx.Err() != nil {
				<-sem
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				samples, err := sc.Scan(ctx, e.org, repo)
				mu.Lock()
				results = append(results, scanResult{scanner: sc.Name(), repo: repo, samples: samples, err: err})
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	return results
}
