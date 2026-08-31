package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// withMockGitHubAPI points githubAPIBase at a local test server for the
// duration of the test, restoring it afterward — the real api.github.com
// is rate-limited and this logic (query construction, JSON parsing) needs
// no real network to verify.
func withMockGitHubAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return srv
}

func TestLastCommitDate_WithSubpath(t *testing.T) {
	var gotPath, gotQuery string
	withMockGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `[{"commit":{"committer":{"date":"2019-03-14T09:26:53Z"}}}]`)
	})

	got, err := LastCommitDate(context.Background(), http.DefaultClient, "owner", "repo", "main", "skills/b2c")
	if err != nil {
		t.Fatalf("LastCommitDate: %v", err)
	}
	want := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
	if gotPath != "/repos/owner/repo/commits" {
		t.Errorf("path = %q", gotPath)
	}
	q, _ := url.ParseQuery(gotQuery)
	if q.Get("sha") != "main" || q.Get("path") != "skills/b2c" || q.Get("per_page") != "1" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestLastCommitDate_NoSubpathOmitsPathParam(t *testing.T) {
	var gotQuery string
	withMockGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `[{"commit":{"committer":{"date":"2019-03-14T09:26:53Z"}}}]`)
	})

	if _, err := LastCommitDate(context.Background(), http.DefaultClient, "owner", "repo", "main", ""); err != nil {
		t.Fatalf("LastCommitDate: %v", err)
	}
	q, _ := url.ParseQuery(gotQuery)
	if _, ok := q["path"]; ok {
		t.Errorf("expected no path param for an empty subpath, got query %q", gotQuery)
	}
}

func TestLastCommitDate_NoCommits(t *testing.T) {
	withMockGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	})

	if _, err := LastCommitDate(context.Background(), http.DefaultClient, "owner", "repo", "main", ""); err == nil {
		t.Fatal("expected an error when GitHub returns no commits")
	}
}

func TestLastCommitDate_HTTPError(t *testing.T) {
	withMockGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // e.g. rate-limited
	})

	if _, err := LastCommitDate(context.Background(), http.DefaultClient, "owner", "repo", "main", ""); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

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
