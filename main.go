// starawesome fetches an awesome list and ranks its GitHub repos by stars.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

type config struct {
	source   string
	format   string
	api      string
	workers  int
	retries  int
	batch    int
	verbose  bool
	headline bool
	version  bool
	stale    string
	rank     rankOptions
}

// version is set for release builds via -ldflags "-X main.version=...".
var version string

func main() {
	cfg := parseFlags()
	if cfg.version {
		fmt.Println("starawesome", versionString())
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.format, "format", "table", "output format: table | md")
	flag.StringVar(&cfg.api, "api", "auto", "GitHub API: auto (graphql if a token is available) | graphql | rest")
	flag.IntVar(&cfg.workers, "workers", 8, "number of concurrent workers")
	flag.IntVar(&cfg.retries, "retries", 5, "max retries per request")
	flag.IntVar(&cfg.batch, "batch", 50, "repos per GraphQL request")
	flag.BoolVar(&cfg.verbose, "v", false, "list every skipped non-GitHub link")
	flag.BoolVar(&cfg.headline, "headline", false, "keep the list's headlines and rank by stars within each (md always does)")
	flag.StringVar(&cfg.rank.sortBy, "sort", "stars", "sort by: stars | commit (newest last commit first)")
	flag.IntVar(&cfg.rank.minStars, "min-stars", 0, "hide repos with fewer stars")
	flag.IntVar(&cfg.rank.top, "top", 0, "show only the first N repos (per headline with -headline/md), 0 = all")
	flag.StringVar(&cfg.stale, "stale", "", "mark repos without a commit for this long, e.g. 90d, 6w, 18m, 2y")
	flag.BoolVar(&cfg.version, "version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] [SOURCE]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "SOURCE: GitHub repo URL, raw markdown URL, local file, or - / empty for stdin.")
		fmt.Fprint(os.Stderr, "Token: $GITHUB_TOKEN, $GH_TOKEN or `gh auth token`.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	cfg.source = flag.Arg(0)
	return cfg
}

func run(ctx context.Context, cfg config) error {
	if cfg.format != "table" && cfg.format != "md" {
		return fmt.Errorf("unknown -format %q (want table or md)", cfg.format)
	}
	if cfg.workers < 1 || cfg.batch < 1 || cfg.retries < 0 {
		return errors.New("-workers and -batch must be >= 1, -retries >= 0")
	}
	if cfg.rank.sortBy != "stars" && cfg.rank.sortBy != "commit" {
		return fmt.Errorf("unknown -sort %q (want stars or commit)", cfg.rank.sortBy)
	}
	if cfg.rank.minStars < 0 || cfg.rank.top < 0 {
		return errors.New("-min-stars and -top must be >= 0")
	}
	staleBefore, err := staleCutoff(cfg.stale, time.Now())
	if err != nil {
		return err
	}
	if flag.NArg() > 1 {
		return fmt.Errorf("expected at most one SOURCE, got %d", flag.NArg())
	}

	token := findToken()
	client := newClient(token, cfg.retries, logf)
	fetch, batch, err := chooseAPI(cfg, token, client)
	if err != nil {
		return err
	}

	md, self, err := readSource(ctx, client, cfg.source)
	if err != nil {
		return err
	}
	parsed := parseMarkdown(md, self)
	if len(parsed.Entries) == 0 {
		return errors.New("no GitHub repositories found in source")
	}

	repos := make([]Repo, len(parsed.Entries))
	for i, e := range parsed.Entries {
		repos[i] = e.Repo
	}
	logf("fetching %d repos with %d workers", len(repos), cfg.workers)
	results := fetchAll(ctx, repos, batch, cfg.workers, fetch, progressPrinter())
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var failed []Result
	for _, repo := range repos {
		if r := results[repo.Key()]; r.Err != nil {
			failed = append(failed, r)
		}
	}
	sections := groupSections(parsed.Entries, results)
	var ranked []Result
	for _, s := range sections {
		ranked = append(ranked, s.Results...)
	}

	if cfg.format == "table" && !cfg.headline {
		sections = []section{{Results: ranked}}
	}
	sections = rankSections(sections, cfg.rank)
	if cfg.format == "md" {
		writeMarkdown(os.Stdout, sections, staleBefore)
	} else {
		writeTable(os.Stdout, sections, staleBefore, isTerminal(os.Stdout))
	}
	report(failed, parsed.Skipped, len(ranked), cfg.verbose)
	return nil
}

// chooseAPI returns the fetcher and its batch size (REST handles one repo per request).
func chooseAPI(cfg config, token string, c *Client) (fetchFunc, int, error) {
	switch {
	case cfg.api == "rest" || (cfg.api == "auto" && token == ""):
		if token == "" {
			logf("warning: no GitHub token found; unauthenticated REST allows only 60 requests/hour")
		}
		return c.fetchREST, 1, nil
	case cfg.api == "graphql" || cfg.api == "auto":
		if token == "" {
			return nil, 0, errors.New("-api graphql requires a token ($GITHUB_TOKEN, $GH_TOKEN or `gh auth login`)")
		}
		return c.fetchGraphQL, cfg.batch, nil
	default:
		return nil, 0, fmt.Errorf("unknown -api %q (want auto, graphql or rest)", cfg.api)
	}
}

// versionString falls back to the module version embedded by `go install`.
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func findToken() string {
	for _, env := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(env)); t != "" {
			return t
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func report(failed []Result, skipped []string, okCount int, verbose bool) {
	logf("done: %d repos ranked, %d failed, %d non-GitHub links skipped", okCount, len(failed), len(skipped))
	for _, r := range failed {
		// Error messages may contain response bodies, so strip control characters.
		logf("  failed %s/%s: %s", r.Repo.Owner, r.Repo.Name, oneLine(r.Err.Error()))
	}
	if len(skipped) == 0 {
		return
	}
	if verbose {
		for _, link := range skipped {
			logf("  skipped %s", oneLine(link))
		}
		return
	}
	hosts := map[string]int{}
	for _, link := range skipped {
		if u, err := url.Parse(link); err == nil {
			hosts[oneLine(strings.ToLower(u.Host))]++
		}
	}
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(hosts[b], hosts[a]), cmp.Compare(a, b))
	})
	logf("skipped links by host (use -v for the full list):")
	for _, h := range names {
		logf("  %5d  %s", hosts[h], h)
	}
}

func progressPrinter() func(done, total int) {
	if !stderrIsTerminal {
		return func(int, int) {}
	}
	return func(done, total int) {
		fmt.Fprintf(os.Stderr, "\r\x1b[Kfetched %d/%d", done, total)
		if done == total {
			fmt.Fprintln(os.Stderr)
		}
	}
}

var stderrIsTerminal = isTerminal(os.Stderr)

func logf(format string, args ...any) {
	if stderrIsTerminal {
		// Clear a pending progress line first.
		format = "\r\x1b[K" + format
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
