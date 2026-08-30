package crawler

import (
	"os"
	"testing"
)

func TestParseAwesomeList_RealFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/awesome-list.md")
	if err != nil {
		t.Fatal(err)
	}

	entries := ParseAwesomeList(string(data))
	if len(entries) == 0 {
		t.Fatal("expected at least one entry parsed from the real fixture")
	}

	byName := make(map[string]Entry)
	for _, e := range entries {
		byName[e.Name] = e
	}

	// Spot-check a plain-repo-URL entry (no /tree/ subpath).
	daisyui, ok := byName["daisyui"]
	if !ok {
		t.Fatal("expected to find 'daisyui' entry")
	}
	if daisyui.RepoURL != "https://github.com/saadeghi/daisyui" {
		t.Errorf("daisyui RepoURL = %q", daisyui.RepoURL)
	}
	if daisyui.Author != "saadeghi" || daisyui.AuthorURL != "https://github.com/saadeghi" {
		t.Errorf("daisyui author fields: %+v", daisyui)
	}
	if daisyui.Category != "Dev & Coding" {
		t.Errorf("daisyui Category = %q, want \"Dev & Coding\"", daisyui.Category)
	}
	if daisyui.Tag != "production" {
		t.Errorf("daisyui Tag = %q, want \"production\"", daisyui.Tag)
	}
	if daisyui.Description == "" {
		t.Error("daisyui Description is empty")
	}

	// Spot-check a /tree/{ref}/{subpath} entry (subdirectory within a
	// larger repo), which is common in this list.
	b2c, ok := byName["b2c"]
	if !ok {
		t.Fatal("expected to find 'b2c' entry")
	}
	if b2c.RepoURL != "https://github.com/SalesforceCommerceCloud/b2c-developer-tooling/tree/main/skills/b2c" {
		t.Errorf("b2c RepoURL = %q", b2c.RepoURL)
	}
	if b2c.Category != "Dev & Coding" {
		t.Errorf("b2c Category = %q", b2c.Category)
	}

	// The document has a second, clearly-distinct section listing
	// candidates that are NOT yet plugins (no plugin.json) — confirms
	// category tracking correctly follows the h2 heading there too,
	// rather than leaking the last h3 category from the real catalog.
	blenderMCP, ok := byName["ahujasid/blender-mcp"]
	if !ok {
		t.Fatal("expected to find the 'ahujasid/blender-mcp' candidate entry")
	}
	if blenderMCP.Category != "Agent Skills and MCP servers ready to be packaged as plugins" {
		t.Errorf("blenderMCP Category = %q", blenderMCP.Category)
	}
	if blenderMCP.Tag != "mcp" {
		t.Errorf("blenderMCP Tag = %q, want \"mcp\"", blenderMCP.Tag)
	}

	// The table-of-contents links at the top of the document must NOT be
	// mistaken for catalog entries (they lack "by [author]" and a tag).
	if _, ok := byName["What is an Agent Plugin?"]; ok {
		t.Error("table-of-contents link was incorrectly parsed as a catalog entry")
	}
}

func TestParseAwesomeList_Empty(t *testing.T) {
	if entries := ParseAwesomeList(""); entries != nil {
		t.Errorf("expected nil for empty input, got %v", entries)
	}
}
