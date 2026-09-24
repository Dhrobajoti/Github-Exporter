package scanners

import (
	"context"

	"github.com/google/go-github/v88/github"

	"github-exporter/internal/config"
	"github-exporter/internal/ghclient"
)

// CodeScanning counts code scanning (CodeQL and third-party SARIF) alerts by
// severity, state and tool.
type CodeScanning struct {
	client *github.Client
	opts   Options
}

func (c *CodeScanning) Name() string { return config.ScannerCodeScanning }

func (c *CodeScanning) Metric() Metric {
	return Metric{
		Name:   "github_code_scanning_alerts",
		Help:   "Number of code scanning alerts per repository, severity, state, and tool",
		Labels: []string{"severity", "state", "tool"},
	}
}

// codeScanningSeverity prefers the security severity (critical/high/medium/low) that
// GitHub assigns to security rules, and falls back to the rule severity
// (error/warning/note) for code-quality rules.
func codeScanningSeverity(a *github.Alert) string {
	if s := a.GetRule().GetSecuritySeverityLevel(); s != "" {
		return s
	}
	return orUnknown(a.GetRule().GetSeverity())
}

func (c *CodeScanning) Scan(ctx context.Context, org, repo string) ([]Sample, error) {
	// The API filters by a single state and, when none is given, lists only
	// open alerts, so request each group explicitly. "closed" covers both
	// fixed and dismissed; every alert still reports its own state.
	states := []string{"open"}
	if c.opts.IncludeClosed {
		states = append(states, "closed")
	}

	t := newTally()
	for _, state := range states {
		err := ghclient.Paginate(ctx,
			func(p ghclient.Page) ([]*github.Alert, *github.Response, error) {
				return c.client.CodeScanning.ListAlertsForRepo(ctx, org, repo, &github.AlertListOptions{
					State:             state,
					ListCursorOptions: p.Cursor(),
					ListOptions:       p.Offset(),
				})
			},
			func(a *github.Alert) {
				t.add(codeScanningSeverity(a), orUnknown(a.GetState()), orUnknown(a.GetTool().GetName()))
			},
		)
		if err != nil {
			return nil, ghclient.Classify(err)
		}
	}
	return t.samples(), nil
}
