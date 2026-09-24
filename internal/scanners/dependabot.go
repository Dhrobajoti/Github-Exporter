package scanners

import (
	"context"

	"github.com/google/go-github/v88/github"

	"github-exporter/internal/config"
	"github-exporter/internal/ghclient"
)

// Dependabot counts Dependabot alerts by severity and state.
type Dependabot struct {
	client *github.Client
	opts   Options
}

func (d *Dependabot) Name() string { return config.ScannerDependabot }

func (d *Dependabot) Metric() Metric {
	return Metric{
		Name:   "github_dependabot_alerts",
		Help:   "Number of Dependabot alerts per repository, severity, and state",
		Labels: []string{"severity", "state"},
	}
}

func (d *Dependabot) Scan(ctx context.Context, org, repo string) ([]Sample, error) {
	state := "open"
	if d.opts.IncludeClosed {
		state = "open,fixed,dismissed,auto_dismissed"
	}

	t := newTally()
	err := ghclient.Paginate(ctx,
		func(p ghclient.Page) ([]*github.DependabotAlert, *github.Response, error) {
			return d.client.Dependabot.ListRepoAlerts(ctx, org, repo, &github.ListAlertsOptions{
				State:             &state,
				ListCursorOptions: p.Cursor(),
				ListOptions:       p.Offset(),
			})
		},
		func(a *github.DependabotAlert) {
			t.add(orUnknown(a.GetSecurityAdvisory().GetSeverity()), orUnknown(a.GetState()))
		},
	)
	if err != nil {
		return nil, ghclient.Classify(err)
	}
	return t.samples(), nil
}
