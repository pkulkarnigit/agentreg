package main

import "testing"

func TestParseRef(t *testing.T) {
	cases := []struct {
		in                   string
		scope, name, version string
		wantErr              bool
	}{
		{in: "@alice/hello", scope: "alice", name: "hello", version: ""},
		{in: "@alice/hello@1.0.0", scope: "alice", name: "hello", version: "1.0.0"},
		{in: "@alice/hello@latest", scope: "alice", name: "hello", version: "latest"},
		{in: "alice/hello", wantErr: true},
		{in: "@alice", wantErr: true},
		{in: "@/hello", wantErr: true},
		{in: "@alice/", wantErr: true},
	}
	for _, c := range cases {
		scope, name, version, err := parseRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRef(%q): expected error, got scope=%q name=%q version=%q", c.in, scope, name, version)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRef(%q): unexpected error: %v", c.in, err)
			continue
		}
		if scope != c.scope || name != c.name || version != c.version {
			t.Errorf("parseRef(%q) = (%q, %q, %q), want (%q, %q, %q)", c.in, scope, name, version, c.scope, c.name, c.version)
		}
	}
}
