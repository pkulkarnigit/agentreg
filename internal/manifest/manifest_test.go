package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

const validPlugin = `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "hello-world",
  "version": "1.0.0",
  "description": "A test plugin",
  "keywords": ["test", "example"]
}`

func TestValidatePluginJSON_Valid(t *testing.T) {
	p, err := ValidatePluginJSON([]byte(validPlugin))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "hello-world" || p.Version != "1.0.0" {
		t.Fatalf("unexpected decoded plugin: %+v", p)
	}
}

func TestValidatePluginJSON_MissingSchema(t *testing.T) {
	_, err := ValidatePluginJSON([]byte(`{"name": "hello-world"}`))
	if err == nil {
		t.Fatal("expected error for missing $schema")
	}
}

func TestValidatePluginJSON_UnknownField(t *testing.T) {
	_, err := ValidatePluginJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "hello-world",
		"totallyUnknownField": true
	}`))
	if err == nil {
		t.Fatal("expected error for additional property (closed schema)")
	}
}

func TestValidatePluginJSON_BadNameChars(t *testing.T) {
	_, err := ValidatePluginJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "Hello_World"
	}`))
	if err == nil {
		t.Fatal("expected error for uppercase/underscore in name")
	}
}

func TestValidatePluginJSON_DoubleHyphen(t *testing.T) {
	_, err := ValidatePluginJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "hello--world"
	}`))
	if err == nil {
		t.Fatal("expected error for consecutive hyphens in name (spec forbids '--')")
	}
}

func TestValidatePluginJSON_DoubleDot(t *testing.T) {
	_, err := ValidatePluginJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "hello..world"
	}`))
	if err == nil {
		t.Fatal("expected error for consecutive periods in name (spec forbids '..')")
	}
}

const validMCP = `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "example": {
      "type": "stdio",
      "command": "example-server",
      "args": ["--flag"]
    }
  }
}`

func TestValidateMCPJSON_Valid(t *testing.T) {
	if err := ValidateMCPJSON([]byte(validMCP)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMCPJSON_ShellCommandRejectedByStructure(t *testing.T) {
	// The spec requires `command` to be a single executable token; the
	// schema only enforces non-empty string, so this is a documented
	// structural rule that ValidateMCPJSON does not (and per spec cannot,
	// via JSON Schema alone) fully enforce — verified separately.
	err := ValidateMCPJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
		"mcpServers": {
			"bad": {"type": "stdio", "command": "sh -c evil"}
		}
	}`))
	if err != nil {
		t.Fatalf("schema-level validation unexpectedly rejected: %v", err)
	}
}

func TestValidateMCPJSON_UnknownTransport(t *testing.T) {
	err := ValidateMCPJSON([]byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
		"mcpServers": {
			"bad": {"type": "carrier-pigeon", "command": "x"}
		}
	}`))
	if err == nil {
		t.Fatal("expected error for unknown transport type")
	}
}

func TestValidateDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(validPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := ValidateDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !b.HasSkills || b.HasMCP {
		t.Fatalf("unexpected bundle: %+v", b)
	}
	if len(b.Skills) != 1 || b.Skills[0] != "example" {
		t.Fatalf("unexpected skills: %v", b.Skills)
	}
}

func TestValidateDir_MissingSkillMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(validPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No SKILL.md written.

	if _, err := ValidateDir(dir); err == nil {
		t.Fatal("expected error for skill directory missing SKILL.md")
	}
}

func TestValidateDir_NoComponents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(validPlugin), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateDir(dir); err == nil {
		t.Fatal("expected error when neither skills/ nor mcp.json is present")
	}
}
