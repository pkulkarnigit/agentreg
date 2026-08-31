package main

import (
	"flag"
	"fmt"
	"time"
)

// cmdAdminBackfillDate calls the admin-only PATCH endpoint that corrects
// an already-published version's recorded date — the one deliberate hole
// in "versions are immutable," for content (typically mirrored) published
// before its real upstream date was known. Requires being logged in as a
// user the server's KRATE_ADMIN_USERNAMES allowlist actually includes;
// anyone else gets a 403 from the server itself.
func cmdAdminBackfillDate(args []string) error {
	fs := flag.NewFlagSet("admin-backfill-date", flag.ExitOnError)
	registryFlag := fs.String("registry", "", "registry URL (defaults to the one from `krate login`)")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: krate admin-backfill-date @scope/name@version <RFC3339-date>")
	}

	scope, name, version, err := parseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	if version == "" {
		return fmt.Errorf("a specific version is required, e.g. @scope/name@1.0.0")
	}

	dateStr := fs.Arg(1)
	if _, err := time.Parse(time.RFC3339, dateStr); err != nil {
		return fmt.Errorf("date must be RFC3339, e.g. 2024-01-15T09:00:00Z: %w", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	base := *registryFlag
	if base == "" {
		base = cfg.Registry
	}
	if base == "" || cfg.Token == "" {
		return fmt.Errorf("not logged in — run `krate login --registry <url>` first")
	}

	var resp struct {
		PublishedAt string `json:"published_at"`
	}
	url := fmt.Sprintf("%s/v1/admin/plugins/%s/%s/%s", base, scope, name, version)
	if err := doJSON("PATCH", url, cfg.Token, map[string]string{"published_at": dateStr}, &resp); err != nil {
		return fmt.Errorf("backfill failed: %w", err)
	}

	fmt.Printf("Set @%s/%s@%s published_at to %s\n", scope, name, version, resp.PublishedAt)
	return nil
}
