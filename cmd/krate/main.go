// Command krate is the KrateAI CLI: init/validate/pack a plugin directory
// locally, signup/login/publish/install/search/view against a registry
// server, and list/uninstall against krate-lock.json — the record, kept
// in the current directory, of what install has put on disk.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "validate":
		err = cmdValidate(args)
	case "pack":
		err = cmdPack(args)
	case "signup":
		err = cmdSignup(args)
	case "login":
		err = cmdLogin(args)
	case "publish":
		err = cmdPublish(args)
	case "install":
		err = cmdInstall(args)
	case "list":
		err = cmdList(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "search":
		err = cmdSearch(args)
	case "view":
		err = cmdView(args)
	case "verify-email":
		err = cmdVerifyEmail(args)
	case "reset-password":
		err = cmdResetPassword(args)
	case "admin-backfill-date":
		err = cmdAdminBackfillDate(args)
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "krate: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "krate: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `krate - CLI for KrateAI, a registry for Agent Plugins (skills + MCP servers)

Usage:
  krate init [dir]                          scaffold a new plugin
  krate validate [dir]                      validate plugin.json/mcp.json/skills locally
  krate pack [dir]                          produce <name>-<version>.tar.gz
  krate signup --registry <url>             create an account (interactive)
  krate login --registry <url>              log in and store an API token
  krate publish [dir]                       validate, pack, and upload
  krate install @scope/name[@version]       fetch and unpack into agent_plugins/<name>
  krate list                                list plugins installed in this directory
  krate uninstall @scope/name               remove an installed plugin and untrack it
  krate search <query>                      search the registry
  krate view @scope/name[@version]          print resolved metadata
  krate verify-email <token>                confirm your account's email address
  krate reset-password --registry <url>     reset a forgotten password (interactive)
  krate admin-backfill-date @scope/name@version <RFC3339-date>
                                             correct a published version's recorded date (admin only)
`)
}
