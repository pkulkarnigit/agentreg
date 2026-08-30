// Package schema embeds the official Agent Plugins 1.0.0 JSON Schemas and
// exposes compiled validators for plugin.json and mcp.json.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed plugin.schema.json mcp.schema.json
var files embed.FS

const (
	PluginSchemaURL = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPSchemaURL    = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

var (
	once        sync.Once
	pluginSchma *jsonschema.Schema
	mcpSchma    *jsonschema.Schema
	loadErr     error
)

// The official name pattern uses a PCRE negative lookahead ("(?!...)") to
// forbid "--" and "..", which Go's RE2-based regexp engine cannot compile.
// We swap in the RE2-safe base-charset pattern here and enforce the "--"/
// ".." rule separately in manifest.ValidatePluginJSON.
const (
	officialNamePattern = `^(?!.*(?:--|\.\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`
	re2SafeNamePattern  = `^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`
)

func compile() {
	c := jsonschema.NewCompiler()
	for name, url := range map[string]string{
		"plugin.schema.json": PluginSchemaURL,
		"mcp.schema.json":    MCPSchemaURL,
	} {
		b, err := files.ReadFile(name)
		if err != nil {
			loadErr = fmt.Errorf("read %s: %w", name, err)
			return
		}
		b, err = patchNamePattern(b)
		if err != nil {
			loadErr = fmt.Errorf("patch %s: %w", name, err)
			return
		}
		if err := c.AddResource(url, bytes.NewReader(b)); err != nil {
			loadErr = fmt.Errorf("add resource %s: %w", url, err)
			return
		}
	}
	pluginSchma, loadErr = c.Compile(PluginSchemaURL)
	if loadErr != nil {
		return
	}
	mcpSchma, loadErr = c.Compile(MCPSchemaURL)
}

// patchNamePattern rewrites properties.name.pattern (if present and equal to
// the known lookahead-based official pattern) to the RE2-safe base-charset
// pattern, decoding/re-encoding rather than a raw byte replace since the
// pattern is JSON-escaped in the source file.
func patchNamePattern(raw []byte) ([]byte, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	props, ok := doc["properties"].(map[string]interface{})
	if !ok {
		return raw, nil
	}
	nameProp, ok := props["name"].(map[string]interface{})
	if !ok {
		return raw, nil
	}
	if pattern, ok := nameProp["pattern"].(string); ok && pattern == officialNamePattern {
		nameProp["pattern"] = re2SafeNamePattern
		return json.Marshal(doc)
	}
	return raw, nil
}

// Plugin returns the compiled plugin.schema.json validator.
func Plugin() (*jsonschema.Schema, error) {
	once.Do(compile)
	return pluginSchma, loadErr
}

// MCP returns the compiled mcp.schema.json validator.
func MCP() (*jsonschema.Schema, error) {
	once.Do(compile)
	return mcpSchma, loadErr
}
