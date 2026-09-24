package ghclient

import (
	"context"
	"errors"
	"time"

	"github.com/google/go-github/v88/github"
)

const (
	maxRateLimitRetries = 2
	// Waiting longer than this for a rate limit reset fails the call instead;
	// the next scheduled refresh will pick the data up.
	maxRateLimitWait = 15 * time.Minute
	defaultAbuseWait = time.Minute
)

// sleep is replaced in tests.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// rateLimitWait reports how long to wait if err is a primary or secondary
// GitHub rate-limit error.
func rateLimitWait(err error) (time.Duration, bool) {
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		return max(time.Until(rl.Rate.Reset.Time), 0) + time.Second, true
	}
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		if abuse.RetryAfter != nil {
			return *abuse.RetryAfter + time.Second, true
		}
		return defaultAbuseWait, true
	}
	return 0, false
}

// withRetry runs call, waiting out GitHub rate limits and retrying a bounded
// number of times.
func withRetry[T any](ctx context.Context, call func() (T, *github.Response, error)) (T, *github.Response, error) {
	for attempt := 0; ; attempt++ {
		v, resp, err := call()
		if err == nil {
			return v, resp, nil
		}
		wait, limited := rateLimitWait(err)
		if !limited || attempt >= maxRateLimitRetries || wait > maxRateLimitWait {
			return v, resp, err
		}
		if serr := sleep(ctx, wait); serr != nil {
			return v, resp, serr
		}
	}
}
