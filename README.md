# KrateAI

Agent Plugins is a new open spec (Amazon, Cursor, Microsoft, OpenAI, and
Vercel shipped it in August 2026) for packaging Skills and MCP servers into
one folder an agent can load. What the spec doesn't cover is how you'd
actually get a plugin from your machine to someone else's — so that's what
this is. Sign up, publish under your own `@username`, and anyone can
`krate install` it. Think npm, minus the fifteen years of baggage.

Free and MIT licensed. Use the hosted instance, or run your own — it's the
same code either way.

## Run it locally

```bash
cp .env.example .env   # fill in POSTGRES_PASSWORD — see the file for how
docker compose up --build
```

Spins up the real stack: `krate-server` behind Postgres in its own
container, blobs on a persistent volume. Server's at `http://localhost:8080`
— web UI on `/`, API under `/v1`. `docker compose up` refuses to start
without `POSTGRES_PASSWORD` set — no hardcoded default to accidentally
carry into a real deployment.

Don't want Docker? SQLite works fine with zero setup:

```bash
go build -o bin/krate-server ./cmd/krate-server
go build -o bin/krate ./cmd/krate
./bin/krate-server
```

Env vars if you need them: `KRATE_DATA_DIR` (default `./data`), `KRATE_ADDR`
(default `:8080`), `KRATE_DB_DRIVER` (`sqlite` or `postgres`),
`KRATE_DB_DSN` (a file path, or a `postgres://` URL), `KRATE_BLOB_DRIVER`
(`fs` or `s3` — see "Production hardening" below), `KRATE_LOG_LEVEL`
(`info` by default; `debug`, `warn`, `error`, or `trace` — see below).

### Log levels

`krate-server` and `cmd/crawler` both log via the standard `log/slog`
package, gated by `KRATE_LOG_LEVEL`. Default is `info`: routine request
logs, startup/shutdown, and anything an operator watching the service
normally wants to see. `warn` and `error` narrow that further. `debug`
adds the detail you'd want while diagnosing something — config values
resolved at startup, individual publish/search/download steps, SMTP send
attempts — none of it shown by default so normal operation stays quiet.
`trace` exists as infrastructure for the rare spot that needs even finer
detail, but most of the codebase never emits at that level.

```bash
KRATE_LOG_LEVEL=debug ./bin/krate-server
```

A request's log level also scales with its outcome: a 5xx logs at
`error`, a 4xx at `warn`, everything else at `info` — except `/healthz`,
which logs at `debug` so routine health checks don't drown out real
traffic. Every 500 response is backed by a logged `error` line with the
real cause; nothing is a silent "internal error" server-side.

## Using the CLI

```bash
# Scaffold a new plugin
krate init my-plugin
cd my-plugin

# Validate locally, no network involved
krate validate

# Create an account
krate signup --registry http://localhost:8080
krate login  --registry http://localhost:8080

# Publish — validates, packs, uploads under your @username
krate publish

# Find something, install it
krate search my-plugin
krate install @yourusername/my-plugin

# See what's installed here, remove something
krate list
krate uninstall @yourusername/my-plugin
```

`krate install` writes a `krate-lock.json` in whatever directory you ran
it from — the same convention `package-lock.json` follows — recording
what's installed and at which resolved version, regardless of any given
install's `--dir`. `krate list` reads it back; `krate uninstall` deletes
both the installed files and its entry. Reinstalling `@scope/name` (e.g.
to pick up a new version) replaces the install directory outright rather
than unpacking on top of it — otherwise a file the new version dropped
would silently survive from the old one.

There's deliberately no dependency resolution here: Agent Plugins don't
declare dependencies on each other, so there's nothing to resolve — this
is the download-verify-unpack half of a package manager, not a build
system.

Plugins live at `@scope/name`, where scope is your username. That's a
registry convention, not part of the spec itself — `plugin.json`'s own
`name` field has to be a bare name, no `@` or `/` allowed.

### Account recovery

```bash
krate verify-email <token>       # confirm your email — optional, not required to publish
krate reset-password --registry <url>          # interactive: prompts for username, then the token
krate reset-password --token <token>           # already have the token from the email/log — skips straight to the new password prompt
```

By default verification and reset links just get written to the server
log instead of emailed — fine for local use, and you can sign up and
publish without ever touching this. Point `KRATE_SMTP_HOST` at a real mail
provider to actually send them:

```bash
KRATE_SMTP_HOST=smtp.sendgrid.net
KRATE_SMTP_PORT=587                              # default; works with SendGrid, SES, Mailgun, Postmark
KRATE_SMTP_USERNAME=apikey
KRATE_SMTP_PASSWORD=your-smtp-password-or-api-key
KRATE_SMTP_FROM="KrateAI <noreply@yourdomain.com>"
```

`internal/notify.SMTPSender` speaks plain SMTP with opportunistic STARTTLS
and AUTH — the protocol nearly every transactional provider supports, so
this isn't tied to one vendor's SDK. `KRATE_SMTP_USERNAME` can be left
empty to skip AUTH entirely, for an unauthenticated local relay. Leave
`KRATE_SMTP_HOST` unset and nothing changes from the log-only default.

## Production hardening

The Postgres backend (`internal/store/postgres`) implements the exact same
interface as SQLite and runs the identical test suite against it — same
behavior, different database underneath. It's what `docker-compose.yml`
uses by default. Its connection pool is capped (10 open, 5 idle) rather
than left at database/sql's unbounded default — every krate-server replica
holds its own pool, so an unbounded one times N replicas is how you
accidentally take down Postgres by scaling up. 10/replica leaves headroom
for around 8 replicas before Postgres's own `max_connections` (100 by
default) needs raising too.

Search stopped doing a leading-wildcard `LIKE '%...%'` scan — that pattern
can't use an index at all, so it got slower in direct proportion to how
many plugins existed, on every single search request. Both backends now
run real full-text search instead: SQLite via an FTS5 index
(`internal/store/sqlite`), Postgres via a generated `tsvector` column with
a GIN index (`internal/store/postgres`). One real behavior change comes
with it — search now matches whole indexed words, not arbitrary
substrings, which is what actually makes an index usable (and is how
GitHub/npm/PyPI search behave too, not a novel restriction). An empty
query still browses everything, same as before. Keywords stay on a plain
`LIKE`, since a single plugin only ever has a handful.

Blob storage has the same swap available: `internal/store/s3blob`
implements the same `store.BlobStore` interface as the default local-disk
`fsblob`, and runs the identical conformance suite (`internal/store/blobtest`)
against a real S3-compatible endpoint. This is the piece that actually
unblocks horizontal scaling — local disk ties `krate-server` to one
machine, since a second replica can't see what the first one wrote to
disk. Once blobs are in S3 (and metadata's already on Postgres), the
server is fully stateless and safe to run as N replicas behind a load
balancer.

```bash
KRATE_BLOB_DRIVER=s3
KRATE_S3_BUCKET=your-bucket
KRATE_S3_REGION=us-east-1
# Only needed for non-AWS S3-compatible stores (MinIO, R2, Spaces, ...):
KRATE_S3_ENDPOINT=http://localhost:9000
KRATE_S3_FORCE_PATH_STYLE=true
```

Credentials come from the standard AWS chain (env vars, `~/.aws/credentials`,
an instance role) — nothing krate-specific to configure there. To try it
locally against MinIO instead of real S3:

```bash
docker compose --profile s3 up -d minio
KRATE_BLOB_DRIVER=s3 KRATE_S3_BUCKET=krate KRATE_S3_ENDPOINT=http://localhost:9000 \
  KRATE_S3_FORCE_PATH_STYLE=true AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
  ./bin/krate-server
```

Rate limiting and login lockout get the same swap, for the same reason:
they default to in-memory (`internal/api/middleware.RateLimiter`,
`internal/auth.LoginLockout`), which is fine for one instance but means
each replica enforces its own limits independently — ten replicas behind a
load balancer effectively multiply every limit by ten. `RedisLimiter` and
`RedisLockout` implement the same `Limiter`/`Lockout` interfaces backed by
Redis instead, so every replica shares one set of buckets and failure
counts. Both run the identical conformance suites
(`internal/api/middleware/limitertest`, `internal/auth/lockouttest`)
against a real Redis. A Redis outage fails open (requests/logins allowed,
with a logged warning) rather than locking everyone out — this is
defense-in-depth on top of bcrypt password hashing and per-scope publish
authorization, not the only thing standing between the registry and abuse.

```bash
KRATE_REDIS_ADDR=localhost:6379
```

That single env var switches both signup/login/publish rate limiting and
login lockout over to Redis; leave it unset and both stay in-memory. To
try it locally:

```bash
docker compose --profile redis up -d redis
KRATE_REDIS_ADDR=localhost:6379 ./bin/krate-server
```

Beyond that: structured request logging, graceful shutdown on
SIGINT/SIGTERM, SQLite backups via `krate-server -backup <path>` (`pg_dump`
for Postgres), and download counts tracked per version.

None of it touched `internal/registry` — see Layout below for why that's
not a coincidence. `internal/api` picked up a small `Option` hook
(`api.WithLockout`, `api.WithLimiterFactory`) so `cmd/krate-server` can
swap in the Redis-backed pieces above; the routing and handler logic
itself didn't change.

## Layout

Here's how the pieces fit together:

```
cmd/krate-server        registry server entrypoint
cmd/krate                CLI entrypoint
cmd/crawler              discovers + validates public plugins into catalog/ (see below)
internal/manifest        plugin.json / mcp.json parsing & validation
internal/schema           vendored official JSON Schemas (go:embed)
internal/pack             tar.gz pack/unpack
internal/store             storage interfaces
internal/store/sqlite       SQLite implementation (default, zero deps)
internal/store/postgres     Postgres implementation (docker-compose default)
internal/store/fsblob       local-filesystem blob storage (default)
internal/store/s3blob        S3-compatible blob storage (opt-in)
internal/store/storetest     shared conformance suite both metadata backends run
internal/store/blobtest       shared conformance suite both blob backends run
internal/registry         business logic — the only caller of internal/store
internal/notify            pluggable account-recovery message delivery
internal/api               REST handlers (HTTP only, calls internal/registry)
internal/api/middleware      auth check, rate limiting (in-memory or Redis), request logging
internal/api/middleware/limitertest  shared conformance suite both rate limiter backends run
internal/web              read-only server-rendered browsing UI
internal/auth              password hashing, API tokens, login lockout (in-memory or Redis)
internal/auth/lockouttest   shared conformance suite both lockout backends run
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
isn't valid semver, which KrateAI correctly refuses to publish.

Add `-publish` and it'll also mirror every valid entry into a running
registry, all under one dedicated account. It can't publish as anyone else
— the server checks the token owner against the scope on every single
publish, mirror or not.

```bash
# one-time setup: create the mirror account
krate signup --registry http://localhost:8080   # username: github-mirror
krate login  --registry http://localhost:8080
export KRATE_MIRROR_TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.krate/config.json'))['token'])")

./bin/crawler -publish -registry http://localhost:8080 -scope github-mirror
```

Publishing is idempotent, so running the crawler again doesn't break
anything — an unchanged version just comes back as "already published"
(409, not treated as an error). Anything missing a valid semver version
gets skipped with a reason instead of a guess.

Mirrored versions are recorded under the date they actually shipped
upstream (the last commit that touched that specific plugin's path), not
the moment the crawler happened to copy them in — a mirror showing
"published today" for something that's existed for a year is actively
misleading. `PUT /v1/plugins/{scope}/{name}/{version}` takes this as an
optional `?published_at=<RFC3339>` query param; self-reported, the same
way a git commit's author date is, and rejected outright if it's in the
future. The normal `krate publish` path never sets it, so ordinary
publishes are unaffected — the server time at actual publish already *is*
the right answer there.

One catch: resolving the default branch for entries that don't pin one
costs a GitHub API call, and unauthenticated that's capped at 60/hour —
mirroring's own upstream-date lookup costs one more per newly-published
entry. Run the crawler a few times back to back and those entries start
timing out until the limit resets. Two things blunt this: an
already-mirrored version skips the date lookup entirely (a cheap check
against the registry itself, not GitHub, since its `published_at` was
already recorded correctly the first time), and a `GITHUB_TOKEN` would
raise the ceiling a lot — not wired up yet.

Anything mirrored before this existed is stuck showing whatever date it
was mirrored on — `published_at` is otherwise immutable, on purpose, so
there's no ordinary way to fix it after the fact. `KRATE_ADMIN_USERNAMES`
(comma-separated) grants exactly those accounts one narrow exception: a
`PATCH /v1/admin/plugins/{scope}/{name}/{version}` that corrects a
version's recorded date and nothing else — not the checksum, not the
manifest. Nobody's an admin by default; the allowlist is empty unless you
set it.

```bash
KRATE_ADMIN_USERNAMES=you,your-coadmin
```

```bash
krate login --registry <url>   # as one of the allowlisted usernames
krate admin-backfill-date @scope/name@version 2024-01-15T09:00:00Z
```

A scheduled workflow (`.github/workflows/crawl.yml`) re-runs the crawler
daily and commits `catalog/agent-plugins-catalog.json` back to the repo if
anything changed — discovery only, no `-publish`. GitHub's runners have no
way to reach a registry running on your own machine, so mirroring into a
specific live instance stays a manual, local thing for now.

## Tests

```bash
go test ./...
```

The Postgres tests skip themselves cleanly if there's no database to talk
to. To actually run them:

```bash
docker run -d --rm -e POSTGRES_PASSWORD=test -e POSTGRES_DB=krate -p 55432:5432 postgres:16-alpine
KRATE_TEST_POSTGRES_DSN="postgres://postgres:test@localhost:55432/krate?sslmode=disable" go test ./...
```

CI does this on every push, against a real Postgres service container, not
a mock.

The S3 blob tests skip themselves cleanly with no S3-compatible store
reachable. To actually run them:

```bash
docker run -d --rm -p 9000:9000 -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin minio/minio server /data
KRATE_TEST_S3_ENDPOINT="http://localhost:9000" AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin go test ./...
```

Same story for the Redis-backed rate limiter/lockout tests:

```bash
docker run -d --rm -p 6379:6379 redis:7-alpine
KRATE_TEST_REDIS_ADDR="localhost:6379" go test ./...
```

And for `SMTPSender`, against [Mailpit](https://github.com/axllent/mailpit)
(a real SMTP server with an API for inspecting what it received, so the
test confirms an email actually arrived intact — not just that `Send`
returned `nil`):

```bash
docker run -d --rm -p 1025:1025 -p 8025:8025 axllent/mailpit
KRATE_TEST_SMTP_ADDR="localhost:1025" KRATE_TEST_SMTP_HTTP_ADDR="localhost:8025" go test ./...
```

## License

MIT. Use it, fork it, self-host it, whatever.
