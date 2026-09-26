package main

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const descriptionWidth = 80

func sortByStars(results []Result) {
	slices.SortStableFunc(results, func(a, b Result) int {
		if c := cmp.Compare(b.Stars, a.Stars); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName))
	})
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

// writeTable prints a ranking per section (pass a single untitled section for
// a flat ranking). With hyperlinks the repo names become clickable via OSC 8
// (supported by kitty, foot, wezterm, iTerm2, ...).
func writeTable(w io.Writer, sections []section, hyperlinks bool) {
	nameWidth, maxRank := len("REPO"), 0
	for _, s := range sections {
		maxRank = max(maxRank, len(s.Results))
		for _, r := range s.Results {
			nameWidth = max(nameWidth, utf8.RuneCountInString(r.FullName))
		}
	}
	rankWidth := len(strconv.Itoa(maxRank))

	fmt.Fprintf(w, "%*s  %9s  %-*s  %s\n", rankWidth, "#", "STARS", nameWidth, "REPO", "DESCRIPTION")
	for _, s := range sections {
		if s.Title != "" {
			fmt.Fprintf(w, "\n## %s\n", oneLine(s.Title))
		}
		sortByStars(s.Results)
		for i, r := range s.Results {
			// Pad outside the escape sequence so invisible bytes don't break alignment.
			padding := strings.Repeat(" ", nameWidth-utf8.RuneCountInString(r.FullName))
			name := r.FullName
			if hyperlinks {
				name = osc8(r.URL, name)
			}
			desc := truncate(oneLine(r.Description), descriptionWidth)
			if r.Archived {
				desc = "[archived] " + desc
			}
			fmt.Fprintf(w, "%*d  %9s  %s%s  %s\n", rankWidth, i+1, groupThousands(r.Stars), name, padding, desc)
		}
	}
}

// writeMarkdown keeps the list's sections (in original order) and sorts each by stars.
func writeMarkdown(w io.Writer, sections []section) {
	for i, s := range sections {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if s.Title != "" {
			fmt.Fprintf(w, "## %s\n\n", oneLine(s.Title))
		}
		sortByStars(s.Results)
		for _, r := range s.Results {
			line := fmt.Sprintf("- ⭐ %s [%s](%s)", humanStars(r.Stars), r.FullName, r.URL)
			if r.Archived {
				line += " *(archived)*"
			}
			if d := oneLine(r.Description); d != "" {
				line += " — " + d
			}
			fmt.Fprintln(w, line)
		}
	}
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
