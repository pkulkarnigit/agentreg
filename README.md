# AgentReg

A free, open-source, self-hostable registry for [Agent Plugins](https://agent-plugins.org)
— the open packaging standard (published Aug 2026, backed by
Amazon/Cursor/Microsoft/OpenAI/Vercel) that bundles Claude-style Skills and
MCP server configs into one portable directory. The spec deliberately leaves
distribution/publishing/registries to implementers — AgentReg is that layer:
publish, install, search, and browse Agent Plugins, npm-style, with real
accounts so anyone can publish under their own `@scope`. MIT licensed — run
it yourself, or use ours.

## Run it locally

```bash
docker compose up --build
```

This runs the full production-shaped stack: `apreg-server` backed by real
Postgres (not SQLite) in a second container, blob storage on a persistent
volume. The server listens on `http://localhost:8080` — web UI at `/`, REST
API under `/v1`.

Without Docker (SQLite, zero external dependencies):

```bash
go build -o bin/apreg-server ./cmd/apreg-server
go build -o bin/apreg ./cmd/apreg
./bin/apreg-server
```

Server env vars: `APREG_DATA_DIR` (default `./data` — SQLite file + blob
tree), `APREG_ADDR` (default `:8080`), `APREG_DB_DRIVER` (`sqlite` default,
or `postgres`), `APREG_DB_DSN` (SQLite file path override, or a
`postgres://...` URL when `APREG_DB_DRIVER=postgres`).

## Using the CLI

```bash
# Scaffold a new plugin
apreg init my-plugin
cd my-plugin

# Validate locally (no network)
apreg validate

# Create an account and log in
apreg signup --registry http://localhost:8080
apreg login  --registry http://localhost:8080

# Publish (validates, packs, uploads under your own @username scope)
apreg publish

# Discover and install
apreg search my-plugin
apreg install @yourusername/my-plugin
```

Plugins are addressed as `@scope/name`, where `scope` is the publishing
user's username — a registry-level convention, since the Agent Plugins
`plugin.json` `name` field itself must be a bare name per spec (no `@`/`/`).

### Account recovery

```bash
apreg verify-email <token>       # confirm your email (advisory in v1 — see below)
apreg reset-password --registry <url>
```

There's no mail provider wired up yet (no domain to send from) — the
verification/reset link is written to the server log instead of emailed.
Signup and login work fine without ever verifying; see
`internal/notify`'s doc comment for how real SMTP/SES delivery slots in
later without touching any caller.

## Production hardening

- **Postgres backend** — `internal/store/postgres`, the same
  `store.MetadataStore` interface as SQLite, proven via a shared
  conformance test suite (`internal/store/storetest`) both backends run.
  `docker-compose.yml` runs on it by default.
- **Rate limiting** — per-IP on signup/login, per-token on publish
  (`internal/api/middleware/ratelimit.go`).
- **Login lockout** — per-username, independent of source IP, defends
  against distributed brute force (`internal/auth/lockout.go`).
- **Structured logging** — every request logged (method, path, status,
  latency, remote addr) via `log/slog`.
- **Graceful shutdown** — `SIGINT`/`SIGTERM` drains in-flight requests
  before exit.
- **Backups** — `apreg-server -backup <path>` writes a consistent SQLite
  snapshot via `VACUUM INTO`; for Postgres, use `pg_dump`. Blob storage is
  plain files — `rsync`/`tar` it.
- **Download counts** — tracked per version, shown on the web UI, a
  cheap trust signal.

None of this required touching `internal/api`'s route handlers' business
logic or `internal/registry` — see "Layout" below for why.

## Layout

```
cmd/apreg-server        registry server entrypoint
cmd/apreg                CLI entrypoint
cmd/crawler              discovers + validates public plugins into catalog/ (see below)
internal/manifest        plugin.json / mcp.json parsing & validation
internal/schema           vendored official JSON Schemas (go:embed)
internal/pack             tar.gz pack/unpack
internal/store             storage interfaces
internal/store/sqlite       SQLite implementation (default, zero deps)
internal/store/postgres     Postgres implementation (docker-compose default)
internal/store/fsblob       local-filesystem blob storage
internal/store/storetest     shared conformance suite both backends run
internal/registry         business logic — the only caller of internal/store
internal/notify            pluggable account-recovery message delivery
internal/api               REST handlers (HTTP only, calls internal/registry)
internal/api/middleware      auth check, rate limiting, request logging
internal/web              read-only server-rendered browsing UI
internal/auth              password hashing, API tokens, login lockout
internal/crawler          fetches + validates public plugins for the catalog
```

`api` and `web` only ever call `registry`; `registry` only ever calls the
`store.MetadataStore`/`store.BlobStore` interfaces. Swapping backends (as
already done for Postgres) or local disk for S3 later is a new file
implementing those interfaces, not a rewrite — see the package doc comment
on `internal/store` for details.

## Catalog and mirror: what's publicly out there

`cmd/crawler` discovers real, publicly known Agent Plugins (from the
[awesome-agent-plugins](https://github.com/ZeroPointRepo/awesome-agent-plugins)
directory), fetches each one from GitHub, and validates it against the
exact same rules AgentReg enforces at publish time
(`internal/manifest.ValidateDir`). Browse the results at `/catalog` on a
running server.

```bash
go build -o bin/crawler ./cmd/crawler
./bin/crawler   # writes catalog/agent-plugins-catalog.json
```

The output records, per entry: whether it actually validates, its
resolved plugin name/version, skill list, and whether it ships an MCP
server — plus, for anything that doesn't validate, exactly why (missing
`skills/`+`mcp.json`, a `SKILL.md`-less subdirectory, a schema violation,
or the fetch itself failing). A real run found real, useful signal: several
"Official & Reference" entries point at collection-repo roots rather than
a single plugin (accurate — they're not individually a compliant plugin at
that path), a couple of entries fail MCP schema validation for reasons
worth reporting upstream, and at least one has a version string
(`0.2.0.dev0`) that isn't valid semver — AgentReg correctly refuses to
publish that one.

Pass `-publish` to also mirror every valid entry into a running registry,
under one dedicated account (never impersonating the original author —
the server enforces that the publishing token's owner matches the scope
on every publish, same as any other `apreg publish`):

```bash
# one-time: create the mirror account
apreg signup --registry http://localhost:8080   # username: github-mirror
apreg login  --registry http://localhost:8080
export APREG_MIRROR_TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.apreg/config.json'))['token'])")

./bin/crawler -publish -registry http://localhost:8080 -scope github-mirror
```

Publishing is idempotent — an unchanged upstream version publishes once,
ever; re-running the crawler just confirms it's still there (HTTP 409,
treated as success, not an error). Entries without a valid semver
`version` field are skipped with a clear reason rather than guessing one.

Note: resolving the default branch for entries that don't pin one via
`/tree/{ref}/...` costs one GitHub API call each; unauthenticated, that's
capped at 60/hour. Re-running the crawler several times in quick
succession can hit that limit — those entries report a fetch error until
it resets. A `GITHUB_TOKEN`-authenticated client (not yet wired up) would
raise it to 5,000/hour.

## Tests

```bash
go test ./...
```

The Postgres conformance suite skips cleanly without a reachable database;
to run it too:

```bash
docker run -d --rm -e POSTGRES_PASSWORD=test -e POSTGRES_DB=apreg -p 55432:5432 postgres:16-alpine
APREG_TEST_POSTGRES_DSN="postgres://postgres:test@localhost:55432/apreg?sslmode=disable" go test ./...
```

CI (`.github/workflows/ci.yml`) runs this automatically against a real
Postgres service container on every push.

## License

[MIT](LICENSE) — use it, self-host it, fork it, ship it.
