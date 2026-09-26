package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultAPIBase = "https://api.github.com"
	httpTimeout    = 60 * time.Second
	// GitHub docs: without retry headers, wait at least a minute on a secondary rate limit.
	minRateLimitWait = time.Minute
	maxBackoff       = time.Minute
	// Caps the exponent: baseBackoff<<30 is far beyond maxBackoff but can't overflow.
	maxBackoffShift = 30
)

// HTTPError is a non-retryable HTTP failure (e.g. 404).
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, truncate(e.Body, 200))
}

type Client struct {
	http        *http.Client
	apiBase     string // the token is only sent to this scheme://host
	token       string
	maxRetries  int
	baseBackoff time.Duration
	log         func(format string, args ...any)
}

func newClient(token string, maxRetries int, log func(string, ...any)) *Client {
	return &Client{
		http:        &http.Client{Timeout: httpTimeout},
		apiBase:     defaultAPIBase,
		token:       token,
		maxRetries:  maxRetries,
		baseBackoff: time.Second,
		log:         log,
	}
}

// do sends the request built by build and retries on network errors, 5xx and
// rate limits. build is called per attempt so request bodies can be re-sent.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		// Never leak the token to arbitrary hosts (e.g. user-supplied raw URLs).
		if c.token != "" && c.isAPI(req.URL) {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("User-Agent", "starawesome")

		wait, hasWait := time.Duration(0), false
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
		} else {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case readErr != nil:
				lastErr = readErr
			case isRateLimited(resp.StatusCode, resp.Header, body):
				lastErr = fmt.Errorf("rate limited (HTTP %d)", resp.StatusCode)
				wait, hasWait = rateLimitWait(resp.Header, time.Now())
				if !hasWait {
					wait, hasWait = max(minRateLimitWait, c.backoff(attempt)), true
				}
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				return body, nil
			case resp.StatusCode >= 500:
				lastErr = &HTTPError{Status: resp.StatusCode, Body: string(body)}
			default:
				return nil, &HTTPError{Status: resp.StatusCode, Body: string(body)}
			}
		}

		if attempt >= c.maxRetries {
			return nil, fmt.Errorf("giving up after %d attempts: %w", attempt+1, lastErr)
		}
		if !hasWait {
			wait = c.backoff(attempt)
		}
		c.log("retry %d/%d in %s: %s %s: %v", attempt+1, c.maxRetries, wait.Round(time.Second), req.Method, req.URL, lastErr)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// isAPI reports whether u points to the GitHub API with matching scheme, so the
// token is neither sent to other hosts nor over plain HTTP.
func (c *Client) isAPI(u *url.URL) bool {
	return u.Scheme+"://"+u.Host == c.apiBase
}

// backoff returns exponential backoff with up to 50% jitter.
func (c *Client) backoff(attempt int) time.Duration {
	d := min(c.baseBackoff<<min(attempt, maxBackoffShift), maxBackoff)
	return d + time.Duration(rand.Int64N(int64(d)/2+1))
}

func isRateLimited(status int, h http.Header, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if status == http.StatusForbidden {
		return h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0"
	}
	// GraphQL reports its primary rate limit as HTTP 200 with an error of type RATE_LIMITED.
	if status == http.StatusOK {
		var r struct {
			Errors []struct{ Type string } `json:"errors"`
		}
		if json.Unmarshal(body, &r) == nil {
			for _, e := range r.Errors {
				if e.Type == "RATE_LIMITED" {
					return true
				}
			}
		}
	}
	return false
}

// rateLimitWait derives the wait time from GitHub's rate limit headers.
func rateLimitWait(h http.Header, now time.Time) (time.Duration, bool) {
	if s := h.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil {
			return time.Duration(secs) * time.Second, true
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(now)+time.Second, 0), true
		}
	}
	return 0, false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
