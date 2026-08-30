# AgentReg

Agent Plugins is a new open spec (Amazon, Cursor, Microsoft, OpenAI, and
Vercel shipped it in August 2026) for packaging Skills and MCP servers into
one folder an agent can load. What the spec doesn't cover is how you'd
actually get a plugin from your machine to someone else's — so that's what
this is. Sign up, publish under your own `@username`, and anyone can
`apreg install` it. Think npm, minus the fifteen years of baggage.

Free and MIT licensed. Use the hosted instance, or run your own — it's the
same code either way.

## Run it locally

```bash
docker compose up --build
```

Spins up the real stack: `apreg-server` behind Postgres in its own
container, blobs on a persistent volume. Server's at `http://localhost:8080`
— web UI on `/`, API under `/v1`.

Don't want Docker? SQLite works fine with zero setup:

```bash
go build -o bin/apreg-server ./cmd/apreg-server
go build -o bin/apreg ./cmd/apreg
./bin/apreg-server
```

Env vars if you need them: `APREG_DATA_DIR` (default `./data`), `APREG_ADDR`
(default `:8080`), `APREG_DB_DRIVER` (`sqlite` or `postgres`),
`APREG_DB_DSN` (a file path, or a `postgres://` URL).

## Using the CLI

```bash
# Scaffold a new plugin
apreg init my-plugin
cd my-plugin

# Validate locally, no network involved
apreg validate

# Create an account
apreg signup --registry http://localhost:8080
apreg login  --registry http://localhost:8080

# Publish — validates, packs, uploads under your @username
apreg publish

# Find something, install it
apreg search my-plugin
apreg install @yourusername/my-plugin
```

Plugins live at `@scope/name`, where scope is your username. That's a
registry convention, not part of the spec itself — `plugin.json`'s own
`name` field has to be a bare name, no `@` or `/` allowed.

### Account recovery

```bash
apreg verify-email <token>       # confirm your email — optional, not required to publish
apreg reset-password --registry <url>
```

No mail server behind this yet (no domain to send from), so verification
and reset links just get written to the server log instead of emailed. You
can sign up and publish without ever touching this. Swapping in real SMTP
later is a one-file change — see `internal/notify`.

## Production hardening

The Postgres backend (`internal/store/postgres`) implements the exact same
interface as SQLite and runs the identical test suite against it — same
behavior, different database underneath. It's what `docker-compose.yml`
uses by default.

Beyond that: rate limiting on signup/login/publish, an account lockout that
kicks in after repeated failed logins no matter which IP they came from,
structured request logging, graceful shutdown on SIGINT/SIGTERM, SQLite
backups via `apreg-server -backup <path>` (`pg_dump` for Postgres), and
download counts tracked per version.

None of it touched `internal/api` or `internal/registry` — see Layout below
for why that's not a coincidence.

## Layout

Here's how the pieces fit together:

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

`api` and `web` only ever talk to `registry`. `registry` only ever talks to
the store interfaces. So when Postgres showed up, it was a new file
implementing an interface, not a rewrite — see the doc comment on
`internal/store` if you want the reasoning.

## Catalog and mirror: what's actually out there

`cmd/crawler` goes and finds real Agent Plugins — right now from the
[awesome-agent-plugins](https://github.com/ZeroPointRepo/awesome-agent-plugins)
list — pulls each one from GitHub, and runs it through the same validation
this registry uses at publish time. Results live at `/catalog` on a running
server.

```bash
go build -o bin/crawler ./cmd/crawler
./bin/crawler   # writes catalog/agent-plugins-catalog.json
```

For each entry you get: does it actually validate, its resolved
name/version, what skills it has, whether it ships an MCP server — and if
it doesn't validate, exactly why. The first real run turned up genuinely
useful stuff: a few "Official & Reference" entries point at whole
collection repos rather than a single plugin (true — they're not compliant
at that specific path), a couple fail MCP schema validation for reasons
worth filing upstream, and one has a version string (`0.2.0.dev0`) that
isn't valid semver, which AgentReg correctly refuses to publish.

Add `-publish` and it'll also mirror every valid entry into a running
registry, all under one dedicated account. It can't publish as anyone else
— the server checks the token owner against the scope on every single
publish, mirror or not.

```bash
# one-time setup: create the mirror account
apreg signup --registry http://localhost:8080   # username: github-mirror
apreg login  --registry http://localhost:8080
export APREG_MIRROR_TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.apreg/config.json'))['token'])")

./bin/crawler -publish -registry http://localhost:8080 -scope github-mirror
```

Publishing is idempotent, so running the crawler again doesn't break
anything — an unchanged version just comes back as "already published"
(409, not treated as an error). Anything missing a valid semver version
gets skipped with a reason instead of a guess.

One catch: resolving the default branch for entries that don't pin one
costs a GitHub API call, and unauthenticated that's capped at 60/hour. Run
the crawler a few times back to back and those entries start timing out
until the limit resets. A `GITHUB_TOKEN` would fix this — not wired up yet.

## Tests

```bash
go test ./...
```

The Postgres tests skip themselves cleanly if there's no database to talk
to. To actually run them:

```bash
docker run -d --rm -e POSTGRES_PASSWORD=test -e POSTGRES_DB=apreg -p 55432:5432 postgres:16-alpine
APREG_TEST_POSTGRES_DSN="postgres://postgres:test@localhost:55432/apreg?sslmode=disable" go test ./...
```

CI does this on every push, against a real Postgres service container, not
a mock.

## License

MIT. Use it, fork it, self-host it, whatever.
