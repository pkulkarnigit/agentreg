package crawler

import "testing"

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		url                       string
		owner, repo, ref, subpath string
		wantErr                   bool
	}{
		{
			url:   "https://github.com/saadeghi/daisyui",
			owner: "saadeghi", repo: "daisyui",
		},
		{
			url:   "https://github.com/SalesforceCommerceCloud/b2c-developer-tooling/tree/main/skills/b2c",
			owner: "SalesforceCommerceCloud", repo: "b2c-developer-tooling", ref: "main", subpath: "skills/b2c",
		},
		{
			url:   "https://github.com/huggingface/diffusers/tree/main/.ai",
			owner: "huggingface", repo: "diffusers", ref: "main", subpath: ".ai",
		},
		{
			url:   "https://github.com/github/awesome-copilot/tree/main/plugins/csharp-dotnet-development",
			owner: "github", repo: "awesome-copilot", ref: "main", subpath: "plugins/csharp-dotnet-development",
		},
		{
			url:   "https://github.com/qdrant/mcp-server-qdrant/tree/master/kiro-power",
			owner: "qdrant", repo: "mcp-server-qdrant", ref: "master", subpath: "kiro-power",
		},
		{
			url:     "https://gitlab.com/foo/bar",
			wantErr: true,
		},
		{
			url:     "https://github.com/justowner",
			wantErr: true,
		},
	}

	for _, c := range cases {
		owner, repo, ref, subpath, err := ParseRepoURL(c.url)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRepoURL(%q): expected error, got owner=%q repo=%q ref=%q subpath=%q", c.url, owner, repo, ref, subpath)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepoURL(%q): unexpected error: %v", c.url, err)
			continue
		}
		if owner != c.owner || repo != c.repo || ref != c.ref || subpath != c.subpath {
			t.Errorf("ParseRepoURL(%q) = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
				c.url, owner, repo, ref, subpath, c.owner, c.repo, c.ref, c.subpath)
		}
	}
}
