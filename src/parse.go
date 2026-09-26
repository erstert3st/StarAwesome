package main

import (
	"net/url"
	"regexp"
	"strings"
)

type Repo struct {
	Owner string
	Name  string
}

// Key is case-insensitive because GitHub owner/repo names are.
func (r Repo) Key() string { return strings.ToLower(r.Owner + "/" + r.Name) }

type Entry struct {
	Repo     Repo
	Category string
}

type Parsed struct {
	Entries []Entry  // unique GitHub repos in document order
	Skipped []string // unique non-GitHub links
}

var (
	urlPattern     = regexp.MustCompile(`https?://[^\s()<>\[\]"'` + "`" + `]+`)
	headingPattern = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
	ownerPattern   = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	namePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	mdLinkText     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// Path prefixes on github.com that are not user/org names.
var reservedOwners = map[string]bool{
	"about": true, "apps": true, "collections": true, "contact": true, "customer-stories": true,
	"enterprise": true, "events": true, "explore": true, "features": true, "issues": true,
	"join": true, "login": true, "marketplace": true, "new": true, "notifications": true,
	"orgs": true, "pricing": true, "pulls": true, "search": true, "security": true,
	"settings": true, "site": true, "sponsors": true, "topics": true, "trending": true, "users": true,
}

// parseMarkdown extracts GitHub repos (with their section heading) and
// non-GitHub links from an awesome list. Links to self are dropped.
func parseMarkdown(md string, self *Repo) Parsed {
	var p Parsed
	seenRepos := map[string]bool{}
	seenSkipped := map[string]bool{}
	category := ""
	inCodeBlock := false

	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeBlock = !inCodeBlock
			continue
		}
		// "# ..." inside a code block is a shell comment, not a heading.
		if m := headingPattern.FindStringSubmatch(trimmed); m != nil && !inCodeBlock {
			category = mdLinkText.ReplaceAllString(m[1], "$1")
			continue
		}
		for _, raw := range urlPattern.FindAllString(line, -1) {
			raw = strings.TrimRight(raw, ".,;:!?*_")
			u, err := url.Parse(raw)
			if err != nil {
				continue
			}
			host := strings.ToLower(u.Host)
			if host != "github.com" && host != "www.github.com" {
				if !seenSkipped[raw] {
					seenSkipped[raw] = true
					p.Skipped = append(p.Skipped, raw)
				}
				continue
			}
			repo, ok := repoFromPath(u.Path)
			// GitHub links that are not repos (profiles, topics, ...) are noise, not errors.
			if !ok || seenRepos[repo.Key()] || (self != nil && repo.Key() == self.Key()) {
				continue
			}
			seenRepos[repo.Key()] = true
			p.Entries = append(p.Entries, Entry{Repo: repo, Category: category})
		}
	}
	return p
}

func repoFromPath(path string) (Repo, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return Repo{}, false
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if reservedOwners[strings.ToLower(owner)] || !ownerPattern.MatchString(owner) || !namePattern.MatchString(name) {
		return Repo{}, false
	}
	return Repo{Owner: owner, Name: name}, true
}
