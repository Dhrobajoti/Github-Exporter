package ghclient

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// ErrNotEnabled means the alert feature is not available for a repository
// (disabled, or no data has ever been produced). It is an expected condition,
// not a failure.
var ErrNotEnabled = errors.New("feature not enabled for repository")

// Classify maps GitHub's "feature is off" responses to ErrNotEnabled and
// returns every other error unchanged.
//
//   - 404: secret scanning / code scanning / Dependabot disabled, or no
//     analysis exists yet.
//   - 403 with a "disabled" / "not enabled" message: the same, reported as a
//     permission error by some endpoints. Other 403s (missing token scope,
//     SSO not authorized) are real errors and must stay visible.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return err
	}

	var resp *github.ErrorResponse
	if !errors.As(err, &resp) || resp.Response == nil {
		return err
	}
	switch resp.Response.StatusCode {
	case http.StatusNotFound:
		return ErrNotEnabled
	case http.StatusForbidden:
		msg := strings.ToLower(resp.Message)
		for _, marker := range []string{"disabled", "not enabled", "advanced security", "must be enabled"} {
			if strings.Contains(msg, marker) {
				return ErrNotEnabled
			}
		}
	}
	return err
}
