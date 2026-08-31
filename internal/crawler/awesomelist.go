// Package crawler discovers publicly known Agent Plugins from the
// awesome-agent-plugins directory on GitHub, fetches each one, validates
// it against the same rules KrateAI enforces at publish time
// (internal/manifest), and records the results as a catalog. Optionally
// (see PublishConfig), it also mirrors every valid entry into a running
// KrateAI registry under one dedicated account's scope — never
// impersonating the original author, since the server enforces that the
// publishing token's owner matches the scope on every publish.
package crawler

import (
	"regexp"
	"strings"
)

// AwesomeListURL is the raw README this package parses.
const AwesomeListURL = "https://raw.githubusercontent.com/ZeroPointRepo/awesome-agent-plugins/main/README.md"

// Entry is one listed plugin: everything the awesome-list itself claims
// about it, before we've fetched or validated anything.
type Entry struct {
	Name        string
	Category    string
	RepoURL     string
	Author      string
	AuthorURL   string
	Description string
	Tag         string
}

var (
	// Matches both "## " and "### " headings — the source document nests
	// entries under an h2 section wrapper (e.g. "## The catalog: verified
	// Agent Plugins") with the actual per-entry categories as h3
	// subheadings (e.g. "### Dev & Coding") inside it, and other h2
	// sections (e.g. "## Agent Skills and MCP servers ready to be
	// packaged as plugins") have entries directly under them with no h3.
	// Tracking whichever heading was most recently seen, regardless of
	// level, produces the right category for both shapes.
	categoryRE = regexp.MustCompile(`^#{2,3}\s+(.+?)\s*$`)
	// Matches: - [name](repo-url) by [author](author-url) — description. **[tag]**
	entryRE = regexp.MustCompile(`^-\s*\[([^\]]+)\]\(([^)]+)\)\s+by\s+\[([^\]]+)\]\(([^)]+)\)\s*(?:—|--|-)\s*(.+?)\s*\*\*\\?\[(\w+)\\?\]\*\*\s*$`)
)

// ParseAwesomeList parses the awesome-agent-plugins README's markdown
// into structured entries, one per listed plugin, tagging each with the
// "## Category" section header it appeared under.
func ParseAwesomeList(markdown string) []Entry {
	var entries []Entry
	category := ""
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := categoryRE.FindStringSubmatch(line); m != nil {
			category = strings.TrimSpace(m[1])
			continue
		}
		if m := entryRE.FindStringSubmatch(line); m != nil {
			entries = append(entries, Entry{
				Name:        strings.TrimSpace(m[1]),
				RepoURL:     strings.TrimSpace(m[2]),
				Author:      strings.TrimSpace(m[3]),
				AuthorURL:   strings.TrimSpace(m[4]),
				Description: strings.TrimSpace(m[5]),
				Tag:         strings.TrimSpace(m[6]),
				Category:    category,
			})
		}
	}
	return entries
}
