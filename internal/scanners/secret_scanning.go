package scanners

import (
	"context"

	"github.com/google/go-github/v88/github"

	"github-exporter/internal/config"
	"github-exporter/internal/ghclient"
)

// SecretScanning counts secret scanning alerts by secret type and state.
//
// The API response includes the leaked secret itself. Only the type and state
// are used; the value is never stored, logged or exported.
type SecretScanning struct {
	client *github.Client
	opts   Options
}

func (s *SecretScanning) Name() string { return config.ScannerSecretScanning }

func (s *SecretScanning) Metric() Metric {
	return Metric{
		Name:   "github_secret_scanning_alerts",
		Help:   "Number of secret scanning alerts per repository, secret type, and state",
		Labels: []string{"secret_type", "state"},
	}
}

func (s *SecretScanning) Scan(ctx context.Context, org, repo string) ([]Sample, error) {
	states := []string{"open"}
	if s.opts.IncludeClosed {
		states = append(states, "resolved")
	}

	t := newTally()
	for _, state := range states {
		err := ghclient.Paginate(ctx,
			func(p ghclient.Page) ([]*github.SecretScanningAlert, *github.Response, error) {
				return s.client.SecretScanning.ListAlertsForRepo(ctx, org, repo, &github.SecretScanningAlertListOptions{
					State:             state,
					ListCursorOptions: p.Cursor(),
					ListOptions:       p.Offset(),
				})
			},
			func(a *github.SecretScanningAlert) {
				t.add(orUnknown(a.GetSecretType()), orUnknown(a.GetState()))
			},
		)
		if err != nil {
			return nil, ghclient.Classify(err)
		}
	}
	return t.samples(), nil
}
