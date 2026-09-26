package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseMarkdown(t *testing.T) {
	md := `# Awesome Foo
[![Badge](https://img.shields.io/badge/x.svg)](https://awesome.re)

## Web Frameworks

- [gin](https://github.com/gin-gonic/gin) - fast.
- [Echo](https://github.com/labstack/echo.git), by [someone](https://github.com/someone).
- dup: https://github.com/Gin-Gonic/gin/tree/master
- [Lab](https://gitlab.com/foo/bar)

## [Tools](#tools)

- [x](https://github.com/owner/tool/blob/main/README.md). Sponsor via https://github.com/sponsors/x
- [self](https://github.com/me/awesome-foo/blob/main/CONTRIBUTING.md)
`
	p := parseMarkdown(md, &Repo{Owner: "me", Name: "awesome-foo"})

	want := []Entry{
		{Repo{"gin-gonic", "gin"}, "Web Frameworks"},
		{Repo{"labstack", "echo"}, "Web Frameworks"},
		{Repo{"owner", "tool"}, "Tools"},
	}
	if len(p.Entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", p.Entries, want)
	}
	for i := range want {
		if p.Entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, p.Entries[i], want[i])
		}
	}
	wantSkipped := []string{"https://img.shields.io/badge/x.svg", "https://awesome.re", "https://gitlab.com/foo/bar"}
	if len(p.Skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %v", p.Skipped, wantSkipped)
	}
	for i := range wantSkipped {
		if p.Skipped[i] != wantSkipped[i] {
			t.Errorf("skipped %d = %q, want %q", i, p.Skipped[i], wantSkipped[i])
		}
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Unix(1000, 0)
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", "1030")
	if d, ok := rateLimitWait(h, now); !ok || d != 31*time.Second {
		t.Errorf("reset wait = %v %v, want 31s true", d, ok)
	}
	h.Set("Retry-After", "5")
	if d, ok := rateLimitWait(h, now); !ok || d != 5*time.Second {
		t.Errorf("retry-after wait = %v %v, want 5s true", d, ok)
	}
	if _, ok := rateLimitWait(http.Header{}, now); ok {
		t.Error("expected no wait without headers")
	}
}

func testClient(retries int) *Client {
	c := newClient("", retries, func(string, ...any) {})
	c.baseBackoff = time.Millisecond
	return c
}

func get(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) { return http.NewRequest(http.MethodGet, url, nil) }
}

func TestDoRetriesRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		case 3:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"errors":[{"type":"RATE_LIMITED"}]}`))
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()

	body, err := testClient(5).do(context.Background(), get(srv.URL))
	if err != nil || string(body) != "ok" {
		t.Fatalf("do = %q, %v", body, err)
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("calls = %d, want 4", n)
	}
}

func TestDoGivesUpAndDoesNotRetry404(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		w.WriteHeader(code)
	}))
	defer srv.Close()

	if _, err := testClient(2).do(context.Background(), get(srv.URL+"?code=500")); err == nil {
		t.Fatal("expected error after retries")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("calls after 500s = %d, want 3", n)
	}

	calls.Store(0)
	if _, err := testClient(2).do(context.Background(), get(srv.URL+"?code=404")); err == nil {
		t.Fatal("expected 404 error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls after 404 = %d, want 1", n)
	}
}

func TestFetchAllCollectsEveryRepo(t *testing.T) {
	repos := make([]Repo, 23)
	for i := range repos {
		repos[i] = Repo{"o", strconv.Itoa(i)}
	}
	fake := func(_ context.Context, batch []Repo) []Result {
		out := make([]Result, len(batch))
		for i, r := range batch {
			n, _ := strconv.Atoi(r.Name)
			out[i] = Result{Repo: r, FullName: "o/" + r.Name, Stars: n}
		}
		return out
	}
	got := fetchAll(context.Background(), repos, 5, 4, fake, func(int, int) {})
	if len(got) != len(repos) {
		t.Fatalf("got %d results, want %d", len(got), len(repos))
	}
}

func TestFormatting(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := groupThousands(n); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{42: "42", 12345: "12.3k", 2_500_000: "2.5M"} {
		if got := humanStars(n); got != want {
			t.Errorf("humanStars(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestGroupSections(t *testing.T) {
	a, b, c, old := Repo{"o", "a"}, Repo{"o", "b"}, Repo{"o", "c"}, Repo{"o", "old-a"}
	entries := []Entry{{a, "Tools"}, {b, "Libs"}, {c, "Tools"}, {old, "Misc"}}
	results := map[string]Result{
		a.Key():   {FullName: "o/a"},
		b.Key():   {FullName: "o/b", Err: context.Canceled},
		c.Key():   {FullName: "o/c"},
		old.Key(): {FullName: "O/A"}, // renamed to o/a
	}
	got := groupSections(entries, results)
	if len(got) != 1 || got[0].Title != "Tools" || len(got[0].Results) != 2 {
		t.Fatalf("sections = %+v, want only Tools with 2 results", got)
	}
}

func TestParseMarkdownIgnoresHeadingsInCodeBlocks(t *testing.T) {
	md := "## Tools\n\n```sh\n# install it\ngo install x\n```\n- [a](https://github.com/o/a)\n"
	p := parseMarkdown(md, nil)
	if len(p.Entries) != 1 || p.Entries[0].Category != "Tools" {
		t.Fatalf("entries = %+v, want o/a in Tools", p.Entries)
	}
}

func TestOneLineStripsControlCharacters(t *testing.T) {
	if got := oneLine("a\x1b[2J\tb\u009b31m\n c"); got != "a [2J b 31m c" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestWriteTableStripsEscapes(t *testing.T) {
	var buf bytes.Buffer
	sections := []section{{Title: "T\x1b[2J", Results: []Result{{FullName: "o/a", Description: "hi\x1b]0;x\x07"}}}}
	writeTable(&buf, sections, time.Time{}, false)
	if strings.ContainsAny(buf.String(), "\x1b\x07") {
		t.Errorf("output contains control characters: %q", buf.String())
	}
}

func TestWriteTableShowsLastCommit(t *testing.T) {
	var buf bytes.Buffer
	results := []Result{
		{FullName: "o/a", Stars: 2, LastCommit: time.Date(2021, 3, 4, 0, 0, 0, 0, time.UTC)},
		{FullName: "o/empty", Stars: 1},
	}
	writeTable(&buf, []section{{Results: results}}, time.Time{}, false)
	lines := strings.Split(buf.String(), "\n")
	if !strings.Contains(lines[0], "LAST COMMIT") || !strings.Contains(lines[1], "2021-03-04") || !strings.Contains(lines[2], "o/empty  -") {
		t.Errorf("table =\n%s", buf.String())
	}
}

func TestRankSections(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC) }
	sections := []section{
		{Title: "A", Results: []Result{
			{FullName: "o/old", Stars: 30, LastCommit: day(1)},
			{FullName: "o/new", Stars: 20, LastCommit: day(9)},
			{FullName: "o/empty", Stars: 40},
			{FullName: "o/tiny", Stars: 1, LastCommit: day(20)},
		}},
		{Title: "B", Results: []Result{{FullName: "o/small", Stars: 2}}},
	}
	names := func(s []section) []string {
		var out []string
		for _, sec := range s {
			for _, r := range sec.Results {
				out = append(out, sec.Title+":"+r.FullName)
			}
		}
		return out
	}
	for _, tc := range []struct {
		opts rankOptions
		want []string
	}{
		{rankOptions{sortBy: "stars"}, []string{"A:o/empty", "A:o/old", "A:o/new", "A:o/tiny", "B:o/small"}},
		{rankOptions{sortBy: "commit"}, []string{"A:o/tiny", "A:o/new", "A:o/old", "A:o/empty", "B:o/small"}},
		{rankOptions{sortBy: "stars", minStars: 5}, []string{"A:o/empty", "A:o/old", "A:o/new"}},
		{rankOptions{sortBy: "commit", minStars: 2, top: 1}, []string{"A:o/new", "B:o/small"}},
	} {
		if got := names(rankSections(sections, tc.opts)); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.opts, got, tc.want)
		}
	}
}

func TestStaleCutoff(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for age, want := range map[string]time.Time{
		"":    {},
		"10d": time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC),
		"2w":  time.Date(2026, 3, 17, 12, 0, 0, 0, time.UTC),
		"6m":  time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC), // Sep 31 normalizes to Oct 1
		"2y":  time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC),
	} {
		if got, err := staleCutoff(age, now); err != nil || !got.Equal(want) {
			t.Errorf("staleCutoff(%q) = %v, %v; want %v", age, got, err, want)
		}
	}
	for _, bad := range []string{"2", "y", "-1y", "1.5y", "3h", "99999999999999999999d"} {
		if _, err := staleCutoff(bad, now); err == nil {
			t.Errorf("staleCutoff(%q): expected error", bad)
		}
	}
}

func TestStaleMarking(t *testing.T) {
	cutoff := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sections := []section{{Results: []Result{
		{FullName: "o/old", LastCommit: cutoff.AddDate(0, 0, -1)},
		{FullName: "o/fresh", LastCommit: cutoff.AddDate(0, 0, 1)},
		{FullName: "o/empty"},
	}}}
	var table, md bytes.Buffer
	writeTable(&table, sections, cutoff, false)
	writeMarkdown(&md, sections, cutoff)
	if got := strings.Count(table.String(), "[stale]"); got != 1 || !strings.Contains(table.String(), "o/old    2023-12-31   [stale]") {
		t.Errorf("table =\n%s", table.String())
	}
	if got := strings.Count(md.String(), "*(stale)*"); got != 1 || !strings.Contains(md.String(), "[o/old]() *(stale)*") {
		t.Errorf("md =\n%s", md.String())
	}
	var tty bytes.Buffer
	writeTable(&tty, sections, cutoff, true)
	if !strings.Contains(tty.String(), "\x1b[2m") {
		t.Errorf("terminal output not dimmed: %q", tty.String())
	}
}

func TestBackoffDoesNotOverflow(t *testing.T) {
	c := newClient("", 100, func(string, ...any) {})
	for attempt := range 100 {
		if d := c.backoff(attempt); d < c.baseBackoff || d > maxBackoff*3/2 {
			t.Fatalf("backoff(%d) = %v", attempt, d)
		}
	}
}

// apiServer returns a client whose API base points to a test server running h.
func apiServer(t *testing.T, token string, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := testClient(0)
	c.token, c.apiBase = token, srv.URL
	return c
}

func TestDoSendsTokenOnlyToAPI(t *testing.T) {
	var gotAuth atomic.Value
	handler := func(w http.ResponseWriter, r *http.Request) { gotAuth.Store(r.Header.Get("Authorization")) }
	c := apiServer(t, "secret", handler)
	other := httptest.NewServer(http.HandlerFunc(handler))
	defer other.Close()

	if _, err := c.do(context.Background(), get(c.apiBase+"/x")); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "Bearer secret" {
		t.Errorf("API auth = %q, want Bearer secret", got)
	}
	if _, err := c.do(context.Background(), get(other.URL+"/x")); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "" {
		t.Errorf("foreign host got auth %q", got)
	}
}

func TestFetchREST(t *testing.T) {
	c := apiServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/a" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"full_name":"o/a","html_url":"https://github.com/o/a","stargazers_count":7,"description":"d","archived":true,"pushed_at":"2024-05-06T07:08:09Z"}`))
	})
	got := c.fetchREST(context.Background(), []Repo{{"o", "a"}, {"o", "missing"}})
	want := Result{Repo: Repo{"o", "a"}, FullName: "o/a", URL: "https://github.com/o/a", Stars: 7, Description: "d", Archived: true,
		LastCommit: time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)}
	if got[0] != want {
		t.Errorf("result 0 = %+v, want %+v", got[0], want)
	}
	if got[1].Err == nil {
		t.Error("expected error for missing repo")
	}
}

func TestFetchGraphQL(t *testing.T) {
	c := apiServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Variables map[string]string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.URL.Path != "/graphql" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Variables["o0"] != "o" || req.Variables["n1"] != "gone" {
			http.Error(w, "unexpected variables", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{
			"data": {"r0": {"nameWithOwner":"o/a","url":"https://github.com/o/a","stargazerCount":3,"description":"d","isArchived":false,"defaultBranchRef":{"target":{"committedDate":"2023-01-02T03:04:05Z"}}}, "r1": null, "r2": null},
			"errors": [{"type":"NOT_FOUND","message":"Could not resolve","path":["r1"]}]
		}`))
	})
	got := c.fetchGraphQL(context.Background(), []Repo{{"o", "a"}, {"o", "gone"}, {"o", "b"}})
	if got[0].FullName != "o/a" || got[0].Stars != 3 || got[0].Err != nil || formatDate(got[0].LastCommit) != "2023-01-02" {
		t.Errorf("result 0 = %+v", got[0])
	}
	if got[1].Err == nil || got[1].Err.Error() != "Could not resolve" {
		t.Errorf("result 1 err = %v, want alias error", got[1].Err)
	}
	if got[2].Err == nil {
		t.Error("result 2: expected not found error")
	}
}

func TestFetchGraphQLFailsWholeBatchWithoutData(t *testing.T) {
	c := apiServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"type":"INTERNAL","message":"boom"}]}`))
	})
	for _, r := range c.fetchGraphQL(context.Background(), []Repo{{"o", "a"}, {"o", "b"}}) {
		if r.Err == nil || r.Err.Error() != "boom" {
			t.Errorf("%v: err = %v, want boom", r.Repo, r.Err)
		}
	}
}

func TestReadSourceGitHubReadme(t *testing.T) {
	c := apiServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/me/awesome/readme" || r.Header.Get("Accept") != "application/vnd.github.raw" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("# list"))
	})
	md, self, err := readSource(context.Background(), c, "https://github.com/me/awesome/")
	if err != nil || md != "# list" || self == nil || *self != (Repo{"me", "awesome"}) {
		t.Fatalf("readSource = %q, %v, %v", md, self, err)
	}
}

func TestReadSourceLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list.md")
	if err := os.WriteFile(path, []byte("# local"), 0o600); err != nil {
		t.Fatal(err)
	}
	md, self, err := readSource(context.Background(), testClient(0), path)
	if err != nil || md != "# local" || self != nil {
		t.Fatalf("readSource = %q, %v, %v", md, self, err)
	}
}
