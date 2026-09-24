// Package scanners turns GitHub security alerts into aggregated counts, one
// Scanner per alert type.
package scanners

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v88/github"

	"github-exporter/internal/config"
)

// Metric describes the Prometheus gauge a Scanner feeds. Labels does not
// include "repo", which the exporter always adds as the first label.
type Metric struct {
	Name   string
	Help   string
	Labels []string
}

// Sample is the number of alerts sharing one combination of label values,
// ordered like Metric.Labels.
type Sample struct {
	Labels []string
	Count  float64
}

// Scanner counts one type of security alert for a single repository.
type Scanner interface {
	// Name is the stable identifier used in configuration, logs and the
	// scanner label of the exporter's own error metric.
	Name() string
	Metric() Metric
	// Scan returns ghclient.ErrNotEnabled when the feature is off for the
	// repository.
	Scan(ctx context.Context, org, repo string) ([]Sample, error)
}

// Options are shared by all scanners.
type Options struct {
	// IncludeClosed also counts fixed, dismissed and resolved alerts. When
	// false only open alerts are counted.
	IncludeClosed bool
}

// New builds the scanners named in names, in that order.
func New(names []string, client *github.Client, opts Options) ([]Scanner, error) {
	out := make([]Scanner, 0, len(names))
	for _, name := range names {
		switch name {
		case config.ScannerDependabot:
			out = append(out, &Dependabot{client: client, opts: opts})
		case config.ScannerCodeScanning:
			out = append(out, &CodeScanning{client: client, opts: opts})
		case config.ScannerSecretScanning:
			out = append(out, &SecretScanning{client: client, opts: opts})
		default:
			return nil, fmt.Errorf("unknown scanner %q", name)
		}
	}
	return out, nil
}

// tally counts alerts per label combination.
type tally struct {
	counts map[string]*Sample
}

func newTally() *tally { return &tally{counts: map[string]*Sample{}} }

func (t *tally) add(labels ...string) {
	key := strings.Join(labels, "\x00")
	if s, ok := t.counts[key]; ok {
		s.Count++
		return
	}
	t.counts[key] = &Sample{Labels: labels, Count: 1}
}

// samples returns the counts in a stable order.
func (t *tally) samples() []Sample {
	out := make([]Sample, 0, len(t.counts))
	for _, s := range t.counts {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].Labels, "\x00") < strings.Join(out[j].Labels, "\x00")
	})
	return out
}

// orUnknown keeps label values non-empty so series stay queryable.
func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
