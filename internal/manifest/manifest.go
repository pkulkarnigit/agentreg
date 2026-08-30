// Package manifest parses and validates plugin.json / mcp.json and the
// directory layout defined by the Agent Plugins 1.0.0 specification.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pkulkarni/apreg/internal/schema"
)

// Plugin mirrors the fields of plugin.json needed by the registry.
type Plugin struct {
	Schema      string          `json:"$schema"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage"`
	Repository  string          `json:"repository"`
	License     string          `json:"license"`
	Keywords    []string        `json:"keywords"`
	Author      json.RawMessage `json:"author,omitempty"`
}

// nameRE mirrors the plugin.schema.json pattern for the "name" field.
var nameRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)$`)

// ValidatePluginJSON validates raw plugin.json bytes against the official
// schema and returns the decoded manifest.
func ValidatePluginJSON(raw []byte) (*Plugin, error) {
	s, err := schema.Plugin()
	if err != nil {
		return nil, fmt.Errorf("load plugin schema: %w", err)
	}

	var doc interface{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("plugin.json is not valid JSON: %w", err)
	}

	if err := s.Validate(doc); err != nil {
		return nil, fmt.Errorf("plugin.json failed schema validation: %w", err)
	}

	var p Plugin
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode plugin.json: %w", err)
	}

	if !nameRE.MatchString(p.Name) || containsDoubleSeparator(p.Name) {
		return nil, fmt.Errorf("plugin.json name %q does not satisfy the spec's naming rules", p.Name)
	}

	return &p, nil
}

func containsDoubleSeparator(name string) bool {
	for i := 0; i+1 < len(name); i++ {
		if (name[i] == '-' && name[i+1] == '-') || (name[i] == '.' && name[i+1] == '.') {
			return true
		}
	}
	return false
}

// ValidateMCPJSON validates raw mcp.json bytes against the official schema.
func ValidateMCPJSON(raw []byte) error {
	s, err := schema.MCP()
	if err != nil {
		return fmt.Errorf("load mcp schema: %w", err)
	}

	var doc interface{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("mcp.json is not valid JSON: %w", err)
	}

	if err := s.Validate(doc); err != nil {
		return fmt.Errorf("mcp.json failed schema validation: %w", err)
	}
	return nil
}

// Bundle is a fully validated plugin directory: parsed manifest plus which
// optional components are present.
type Bundle struct {
	Plugin    *Plugin
	HasSkills bool
	HasMCP    bool
	Skills    []string // skill directory names
}

// ValidateDir validates a plugin directory on disk: plugin.json against its
// schema, mcp.json against its schema (if present), and the structural rule
// that every skills/*/ subdirectory contains a SKILL.md, with at least one
// of skills/ or mcp.json present.
func ValidateDir(dir string) (*Bundle, error) {
	manifestPath := filepath.Join(dir, "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read plugin.json: %w", err)
	}
	p, err := ValidatePluginJSON(raw)
	if err != nil {
		return nil, err
	}

	b := &Bundle{Plugin: p}

	skillsDir := filepath.Join(dir, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			if _, err := os.Stat(skillMD); err != nil {
				if errIsNotExist(err) {
					return nil, fmt.Errorf("skills/%s is missing SKILL.md", e.Name())
				}
				return nil, fmt.Errorf("stat %s: %w", skillMD, err)
			}
			b.Skills = append(b.Skills, e.Name())
		}
		b.HasSkills = len(b.Skills) > 0
	}

	mcpPath := filepath.Join(dir, "mcp.json")
	if raw, err := os.ReadFile(mcpPath); err == nil {
		if err := ValidateMCPJSON(raw); err != nil {
			return nil, err
		}
		b.HasMCP = true
	} else if !errIsNotExist(err) {
		return nil, fmt.Errorf("read mcp.json: %w", err)
	}

	if !b.HasSkills && !b.HasMCP {
		return nil, fmt.Errorf("plugin has neither a skills/ directory nor an mcp.json — at least one component is required")
	}

	return b, nil
}

func errIsNotExist(err error) bool {
	return os.IsNotExist(err)
}
