package schema

import "testing"

func TestCompile(t *testing.T) {
	if _, err := Plugin(); err != nil {
		t.Fatalf("Plugin(): %v", err)
	}
	if _, err := MCP(); err != nil {
		t.Fatalf("MCP(): %v", err)
	}
}
