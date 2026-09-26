package main

import (
	"cmp"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const descriptionWidth = 80

// rankOptions controls filtering and ordering within each section.
type rankOptions struct {
	sortBy   string // "stars" or "commit"
	minStars int
	top      int // 0 keeps all
}

// rankSections filters and sorts each section, keeps its first top repos and
// drops sections that end up empty.
func rankSections(sections []section, opts rankOptions) []section {
	var out []section
	for _, s := range sections {
		var kept []Result
		for _, r := range s.Results {
			if r.Stars >= opts.minStars {
				kept = append(kept, r)
			}
		}
		sortResults(kept, opts.sortBy)
		if opts.top > 0 && len(kept) > opts.top {
			kept = kept[:opts.top]
		}
		if len(kept) > 0 {
			out = append(out, section{Title: s.Title, Results: kept})
		}
	}
	return out
}

// sortResults orders by stars or by last commit (newest first, unknown last),
// breaking ties by stars and then name.
func sortResults(results []Result, sortBy string) {
	slices.SortStableFunc(results, func(a, b Result) int {
		byCommit := 0
		if sortBy == "commit" {
			byCommit = b.LastCommit.Compare(a.LastCommit)
		}
		return cmp.Or(
			byCommit,
			cmp.Compare(b.Stars, a.Stars),
			cmp.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName)),
		)
	})
}

var agePattern = regexp.MustCompile(`^(\d+)([dwmy])$`)

// staleCutoff turns an age like "90d", "6w", "18m" or "2y" into the point in
// time before which a last commit counts as stale. An empty age disables it.
func staleCutoff(age string, now time.Time) (time.Time, error) {
	if age == "" {
		return time.Time{}, nil
	}
	m := agePattern.FindStringSubmatch(age)
	if m == nil {
		return time.Time{}, fmt.Errorf("invalid -stale %q (want e.g. 90d, 6w, 18m or 2y)", age)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid -stale %q: %w", age, err)
	}
	switch m[2] {
	case "d":
		return now.AddDate(0, 0, -n), nil
	case "w":
		return now.AddDate(0, 0, -7*n), nil
	case "m":
		return now.AddDate(0, -n, 0), nil
	default:
		return now.AddDate(-n, 0, 0), nil
	}
}

// isStale reports whether r's last known commit is older than cutoff; repos
// without a known commit date are never stale.
func isStale(r Result, cutoff time.Time) bool {
	return !cutoff.IsZero() && !r.LastCommit.IsZero() && r.LastCommit.Before(cutoff)
}

// section is one headline of the list with its repos.
type section struct {
	Title   string
	Results []Result
}

// groupSections keeps the list's sections in original order. Renamed repos
// resolving to the same target are kept only at their first occurrence.
func groupSections(entries []Entry, results map[string]Result) []section {
	var sections []section
	index := map[string]int{}
	seenRepos := map[string]bool{}
	for _, e := range entries {
		r, ok := results[e.Repo.Key()]
		if !ok || r.Err != nil || seenRepos[strings.ToLower(r.FullName)] {
			continue
		}
		seenRepos[strings.ToLower(r.FullName)] = true
		i, seen := index[e.Category]
		if !seen {
			i = len(sections)
			index[e.Category] = i
			sections = append(sections, section{Title: e.Category})
		}
		sections[i].Results = append(sections[i].Results, r)
	}
	return sections
}

// writeTable prints ranked sections (pass a single untitled section for a flat
// ranking). On a terminal the repo names become clickable via OSC 8 (supported
// by kitty, foot, wezterm, iTerm2, ...) and stale repos are dimmed.
func writeTable(w io.Writer, sections []section, staleBefore time.Time, terminal bool) {
	nameWidth, maxRank := len("REPO"), 0
	for _, s := range sections {
		maxRank = max(maxRank, len(s.Results))
		for _, r := range s.Results {
			nameWidth = max(nameWidth, utf8.RuneCountInString(r.FullName))
		}
	}
	rankWidth := len(strconv.Itoa(maxRank))

	fmt.Fprintf(w, "%*s  %9s  %-*s  %-11s  %s\n", rankWidth, "#", "STARS", nameWidth, "REPO", "LAST COMMIT", "DESCRIPTION")
	for _, s := range sections {
		if s.Title != "" {
			fmt.Fprintf(w, "\n## %s\n", oneLine(s.Title))
		}
		for i, r := range s.Results {
			// Pad outside the escape sequence so invisible bytes don't break alignment.
			padding := strings.Repeat(" ", nameWidth-utf8.RuneCountInString(r.FullName))
			name := r.FullName
			if terminal {
				name = osc8(r.URL, name)
			}
			stale := isStale(r, staleBefore)
			desc := truncate(oneLine(r.Description), descriptionWidth)
			if stale {
				desc = "[stale] " + desc
			}
			if r.Archived {
				desc = "[archived] " + desc
			}
			line := fmt.Sprintf("%*d  %9s  %s%s  %-11s  %s", rankWidth, i+1, groupThousands(r.Stars), name, padding, formatDate(r.LastCommit), desc)
			if terminal && stale {
				line = "\x1b[2m" + line + "\x1b[22m"
			}
			fmt.Fprintln(w, line)
		}
	}
}

// writeMarkdown prints ranked sections as a markdown list.
func writeMarkdown(w io.Writer, sections []section, staleBefore time.Time) {
	for i, s := range sections {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if s.Title != "" {
			fmt.Fprintf(w, "## %s\n\n", oneLine(s.Title))
		}
		for _, r := range s.Results {
			line := fmt.Sprintf("- ⭐ %s [%s](%s)", humanStars(r.Stars), r.FullName, r.URL)
			if r.Archived {
				line += " *(archived)*"
			}
			if isStale(r, staleBefore) {
				line += " *(stale)*"
			}
			if !r.LastCommit.IsZero() {
				line += " · last commit " + formatDate(r.LastCommit)
			}
			if d := oneLine(r.Description); d != "" {
				line += " — " + d
			}
			fmt.Fprintln(w, line)
		}
	}
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.DateOnly)
}

func osc8(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// oneLine collapses whitespace and replaces control characters, so untrusted
// text (descriptions, headings) can't inject terminal escape sequences.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// groupThousands formats 1234567 as "1,234,567".
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// humanStars formats 12345 as "12.3k".
func humanStars(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1_000, 'f', 1, 64) + "k"
	default:
		return strconv.Itoa(n)
	}
}
