package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// readSource loads the awesome list from stdin ("" or "-"), a GitHub repo URL
// (its README), any other URL (raw markdown) or a local file. For GitHub repo
// URLs it also returns the repo itself so it can be excluded from the results.
func readSource(ctx context.Context, c *Client, source string) (string, *Repo, error) {
	if source == "" || source == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), nil, err
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		b, err := os.ReadFile(source)
		return string(b), nil, err
	}

	u, err := url.Parse(source)
	if err != nil {
		return "", nil, fmt.Errorf("invalid URL %q: %w", source, err)
	}
	target, accept := source, ""
	var self *Repo
	if host := strings.ToLower(u.Host); host == "github.com" || host == "www.github.com" {
		repo, ok := repoFromPath(u.Path)
		if !ok {
			return "", nil, fmt.Errorf("not a GitHub repository URL: %s", source)
		}
		self = &repo
		target = fmt.Sprintf("%s/repos/%s/%s/readme", c.apiBase, repo.Owner, repo.Name)
		accept = "application/vnd.github.raw"
	}

	body, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err == nil && accept != "" {
			req.Header.Set("Accept", accept)
		}
		return req, err
	})
	if err != nil {
		return "", nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	return string(body), self, nil
}
