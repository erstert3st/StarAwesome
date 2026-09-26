package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Result struct {
	Repo        Repo
	FullName    string
	URL         string
	Stars       int
	Description string
	Archived    bool
	LastCommit  time.Time // zero if unknown (e.g. empty repo)
	Err         error
}

// fetchFunc resolves a batch of repos; it must return one Result per input repo.
type fetchFunc func(ctx context.Context, repos []Repo) []Result

// fetchAll splits repos into batches and processes them with a worker pool.
func fetchAll(ctx context.Context, repos []Repo, batchSize, workers int, fetch fetchFunc, progress func(done, total int)) map[string]Result {
	batches := make(chan []Repo)
	go func() {
		defer close(batches)
		for i := 0; i < len(repos); i += batchSize {
			select {
			case batches <- repos[i:min(i+batchSize, len(repos))]:
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string]Result, len(repos))
	)
	for range workers {
		wg.Go(func() {
			for batch := range batches {
				res := fetch(ctx, batch)
				mu.Lock()
				for _, r := range res {
					results[r.Repo.Key()] = r
				}
				progress(len(results), len(repos))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return results
}

func (c *Client) fetchREST(ctx context.Context, repos []Repo) []Result {
	out := make([]Result, len(repos))
	for i, repo := range repos {
		out[i] = Result{Repo: repo}
		body, err := c.do(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", c.apiBase, repo.Owner, repo.Name), nil)
			if err == nil {
				req.Header.Set("Accept", "application/vnd.github+json")
			}
			return req, err
		})
		if err != nil {
			out[i].Err = err
			continue
		}
		var r struct {
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Stars       int    `json:"stargazers_count"`
			Description string `json:"description"`
			Archived    bool   `json:"archived"`
			// REST would need an extra request per repo for the default
			// branch's last commit; pushed_at is the closest free signal.
			PushedAt time.Time `json:"pushed_at"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			out[i].Err = fmt.Errorf("decode response: %w", err)
			continue
		}
		out[i].FullName, out[i].URL, out[i].Stars, out[i].Description, out[i].Archived, out[i].LastCommit =
			r.FullName, r.HTMLURL, r.Stars, r.Description, r.Archived, r.PushedAt
	}
	return out
}

type graphQLRepo struct {
	NameWithOwner  string `json:"nameWithOwner"`
	URL            string `json:"url"`
	StargazerCount int    `json:"stargazerCount"`
	Description    string `json:"description"`
	IsArchived     bool   `json:"isArchived"`
	// DefaultBranchRef is nil for empty repos.
	DefaultBranchRef *struct {
		Target struct {
			CommittedDate time.Time `json:"committedDate"`
		} `json:"target"`
	} `json:"defaultBranchRef"`
}

// fetchGraphQL resolves a whole batch with one aliased GraphQL query.
func (c *Client) fetchGraphQL(ctx context.Context, repos []Repo) []Result {
	var params, fields []string
	vars := map[string]string{}
	for i, repo := range repos {
		params = append(params, fmt.Sprintf("$o%d: String!, $n%d: String!", i, i))
		fields = append(fields, fmt.Sprintf("r%d: repository(owner: $o%d, name: $n%d) { nameWithOwner url stargazerCount description isArchived defaultBranchRef { target { ... on Commit { committedDate } } } }", i, i, i))
		vars[fmt.Sprintf("o%d", i)] = repo.Owner
		vars[fmt.Sprintf("n%d", i)] = repo.Name
	}
	payload, err := json.Marshal(map[string]any{
		"query":     fmt.Sprintf("query(%s) { %s }", strings.Join(params, ", "), strings.Join(fields, "\n")),
		"variables": vars,
	})
	if err != nil {
		return failAll(repos, err)
	}

	body, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/graphql", bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return failAll(repos, err)
	}

	var resp struct {
		Data   map[string]*graphQLRepo `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return failAll(repos, fmt.Errorf("decode response: %w", err))
	}
	aliasErrs := map[string]error{}
	for _, e := range resp.Errors {
		if len(e.Path) > 0 {
			if alias, ok := e.Path[0].(string); ok {
				aliasErrs[alias] = errors.New(e.Message)
				continue
			}
		}
		if resp.Data == nil {
			return failAll(repos, errors.New(e.Message))
		}
	}

	out := make([]Result, len(repos))
	for i, repo := range repos {
		alias := fmt.Sprintf("r%d", i)
		out[i] = Result{Repo: repo}
		node := resp.Data[alias]
		switch {
		case node != nil:
			out[i].FullName, out[i].URL, out[i].Stars, out[i].Description, out[i].Archived =
				node.NameWithOwner, node.URL, node.StargazerCount, node.Description, node.IsArchived
			if node.DefaultBranchRef != nil {
				out[i].LastCommit = node.DefaultBranchRef.Target.CommittedDate
			}
		case aliasErrs[alias] != nil:
			out[i].Err = aliasErrs[alias]
		default:
			out[i].Err = errors.New("repository not found")
		}
	}
	return out
}

func failAll(repos []Repo, err error) []Result {
	out := make([]Result, len(repos))
	for i, repo := range repos {
		out[i] = Result{Repo: repo, Err: err}
	}
	return out
}
