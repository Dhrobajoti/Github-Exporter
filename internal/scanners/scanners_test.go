package scanners

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github-exporter/internal/config"
	"github-exporter/internal/ghclient"
	"github-exporter/internal/ghtest"
)

func jsonHandler(routes map[string]http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			h(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, s) }
}

func TestDependabot(t *testing.T) {
	var gotState, base string
	client, base := ghtest.NewClient(t, jsonHandler(map[string]http.HandlerFunc{
		"/repos/acme/api/dependabot/alerts": func(w http.ResponseWriter, r *http.Request) {
			gotState = r.URL.Query().Get("state")
			if r.URL.Query().Get("after") == "" {
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/acme/api/dependabot/alerts?after=next>; rel="next"`, base))
				fmt.Fprint(w, `[
					{"number":1,"state":"open","security_advisory":{"severity":"high"}},
					{"number":2,"state":"open","security_advisory":{"severity":"high"}},
					{"number":3,"state":"fixed","security_advisory":{"severity":"low"}}
				]`)
				return
			}
			fmt.Fprint(w, `[{"number":4,"state":"open","security_advisory":{"severity":"high"}},{"number":5,"state":"dismissed"}]`)
		},
	}))

	s := &Dependabot{client: client, opts: Options{IncludeClosed: true}}
	got, err := s.Scan(context.Background(), "acme", "api")
	require.NoError(t, err)

	assert.Equal(t, []Sample{
		{Labels: []string{"high", "open"}, Count: 3},
		{Labels: []string{"low", "fixed"}, Count: 1},
		{Labels: []string{"unknown", "dismissed"}, Count: 1},
	}, got)
	assert.Equal(t, "open,fixed,dismissed,auto_dismissed", gotState)
	assert.Len(t, s.Metric().Labels, 2)
}

func TestDependabotOpenOnly(t *testing.T) {
	var gotState string
	client, _ := ghtest.NewClient(t, jsonHandler(map[string]http.HandlerFunc{
		"/repos/acme/api/dependabot/alerts": func(w http.ResponseWriter, r *http.Request) {
			gotState = r.URL.Query().Get("state")
			fmt.Fprint(w, `[]`)
		},
	}))

	got, err := (&Dependabot{client: client}).Scan(context.Background(), "acme", "api")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, "open", gotState)
}

func TestCodeScanning(t *testing.T) {
	var states []string
	client, _ := ghtest.NewClient(t, jsonHandler(map[string]http.HandlerFunc{
		"/repos/acme/api/code-scanning/alerts": func(w http.ResponseWriter, r *http.Request) {
			state := r.URL.Query().Get("state")
			states = append(states, state)
			switch state {
			case "open":
				fmt.Fprint(w, `[
					{"number":1,"state":"open","tool":{"name":"CodeQL"},"rule":{"severity":"error","security_severity_level":"critical"}},
					{"number":2,"state":"open","tool":{"name":"CodeQL"},"rule":{"severity":"warning"}},
					{"number":3,"state":"open","tool":{"name":"Trivy"},"rule":{"severity":"error","security_severity_level":"critical"}}
				]`)
			case "closed":
				fmt.Fprint(w, `[
					{"number":4,"state":"fixed","tool":{"name":"CodeQL"},"rule":{"severity":"error","security_severity_level":"critical"}},
					{"number":5,"state":"dismissed","tool":{"name":"CodeQL"},"rule":{"severity":"note"}}
				]`)
			default:
				t.Errorf("unexpected state %q", state)
			}
		},
	}))

	s := &CodeScanning{client: client, opts: Options{IncludeClosed: true}}
	got, err := s.Scan(context.Background(), "acme", "api")
	require.NoError(t, err)

	assert.Equal(t, []string{"open", "closed"}, states)
	assert.Equal(t, []Sample{
		{Labels: []string{"critical", "fixed", "CodeQL"}, Count: 1},
		{Labels: []string{"critical", "open", "CodeQL"}, Count: 1},
		{Labels: []string{"critical", "open", "Trivy"}, Count: 1},
		{Labels: []string{"note", "dismissed", "CodeQL"}, Count: 1},
		{Labels: []string{"warning", "open", "CodeQL"}, Count: 1},
	}, got)
}

func TestSecretScanning(t *testing.T) {
	var states []string
	client, _ := ghtest.NewClient(t, jsonHandler(map[string]http.HandlerFunc{
		"/repos/acme/api/secret-scanning/alerts": func(w http.ResponseWriter, r *http.Request) {
			state := r.URL.Query().Get("state")
			states = append(states, state)
			if state == "open" {
				fmt.Fprint(w, `[
					{"number":1,"state":"open","secret_type":"github_personal_access_token","secret":"placeholder"},
					{"number":2,"state":"open","secret_type":"github_personal_access_token"},
					{"number":3,"state":"open","secret_type":"aws_access_key_id"}
				]`)
				return
			}
			fmt.Fprint(w, `[{"number":4,"state":"resolved","secret_type":"aws_access_key_id"}]`)
		},
	}))

	s := &SecretScanning{client: client, opts: Options{IncludeClosed: true}}
	got, err := s.Scan(context.Background(), "acme", "api")
	require.NoError(t, err)

	assert.Equal(t, []string{"open", "resolved"}, states)
	assert.Equal(t, []Sample{
		{Labels: []string{"aws_access_key_id", "open"}, Count: 1},
		{Labels: []string{"aws_access_key_id", "resolved"}, Count: 1},
		{Labels: []string{"github_personal_access_token", "open"}, Count: 2},
	}, got)
}

func TestSecretScanningOpenOnlyMakesOneRequest(t *testing.T) {
	var calls int
	client, _ := ghtest.NewClient(t, jsonHandler(map[string]http.HandlerFunc{
		"/repos/acme/api/secret-scanning/alerts": func(w http.ResponseWriter, _ *http.Request) {
			calls++
			fmt.Fprint(w, `[]`)
		},
	}))

	_, err := (&SecretScanning{client: client}).Scan(context.Background(), "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestScannersReportNotEnabled(t *testing.T) {
	client, _ := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Secret scanning is disabled on this repository."}`)
	}))

	for _, s := range []Scanner{
		&Dependabot{client: client},
		&CodeScanning{client: client},
		&SecretScanning{client: client},
	} {
		t.Run(s.Name(), func(t *testing.T) {
			_, err := s.Scan(context.Background(), "acme", "api")
			assert.ErrorIs(t, err, ghclient.ErrNotEnabled)
		})
	}
}

func TestScannersSurfaceRealErrors(t *testing.T) {
	client, _ := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))

	_, err := (&CodeScanning{client: client}).Scan(context.Background(), "acme", "api")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ghclient.ErrNotEnabled)
}

func TestNew(t *testing.T) {
	client, _ := ghtest.NewClient(t, nil)

	got, err := New([]string{config.ScannerSecretScanning, config.ScannerDependabot}, client, Options{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, config.ScannerSecretScanning, got[0].Name())
	assert.Equal(t, config.ScannerDependabot, got[1].Name())

	_, err = New([]string{"nope"}, client, Options{})
	assert.Error(t, err)
}

func TestMetricNamesAreDistinct(t *testing.T) {
	client, _ := ghtest.NewClient(t, nil)
	all, err := New(config.AllScanners, client, Options{})
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, s := range all {
		m := s.Metric()
		assert.False(t, seen[m.Name], "duplicate metric %s", m.Name)
		assert.NotContains(t, m.Labels, "repo", "the exporter adds the repo label itself")
		seen[m.Name] = true
	}
}
