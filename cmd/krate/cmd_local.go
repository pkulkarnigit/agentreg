package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/pack"
)

const scaffoldPluginJSON = `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "%s",
  "version": "0.1.0",
  "description": "Describe what this plugin does.",
  "keywords": []
}
`

const scaffoldSkillMD = `---
name: example
description: Replace with a one-line description of when to use this skill.
---

# Example skill

Describe what this skill does and when an agent should use it.
`

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "plugin name (defaults to the directory name)")
	fs.Parse(args)

	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	pluginName := *name
	if pluginName == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		pluginName = filepath.Base(abs)
	}

	pluginPath := filepath.Join(dir, "plugin.json")
	if _, err := os.Stat(pluginPath); err == nil {
		return fmt.Errorf("%s already exists", pluginPath)
	}
	if err := os.WriteFile(pluginPath, []byte(fmt.Sprintf(scaffoldPluginJSON, pluginName)), 0o644); err != nil {
		return err
	}

	skillDir := filepath.Join(dir, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(scaffoldSkillMD), 0o644); err != nil {
		return err
	}

	fmt.Printf("Scaffolded plugin %q in %s\n", pluginName, dir)
	return nil
}

func cmdValidate(args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	b, err := manifest.ValidateDir(dir)
	if err != nil {
		return err
	}
	fmt.Printf("OK: %s@%s\n", b.Plugin.Name, b.Plugin.Version)
	if b.HasSkills {
		fmt.Printf("  skills: %v\n", b.Skills)
	}
	if b.HasMCP {
		fmt.Println("  mcp.json: present and valid")
	}
	return nil
}

func cmdPack(args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	b, err := manifest.ValidateDir(dir)
	if err != nil {
		return err
	}

	out := fmt.Sprintf("%s-%s.tar.gz", b.Plugin.Name, b.Plugin.Version)
	checksum, err := pack.Dir(dir, out)
	if err != nil {
		return err
	}
	fmt.Printf("Wrote %s (sha256:%s)\n", out, checksum)
	return nil
}
