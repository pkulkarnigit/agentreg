# AgentReg

A registry for [Agent Plugins](https://agent-plugins.org) — the open packaging
standard (published Aug 2026, backed by Amazon/Cursor/Microsoft/OpenAI/Vercel)
that bundles Claude-style Skills and MCP server configs into one portable
directory. The spec deliberately leaves distribution/publishing/registries to
implementers — AgentReg is that layer: publish, install, search, and browse
Agent Plugins, npm-style.

## Run it locally

```bash
docker compose up --build
```

The server listens on `http://localhost:8080` — the web UI is at `/`, the
REST API under `/v1`.

Without Docker:

```bash
go build -o bin/apreg-server ./cmd/apreg-server
go build -o bin/apreg ./cmd/apreg
./bin/apreg-server   # APREG_DATA_DIR (default ./data), APREG_ADDR (default :8080)
```

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

## Layout

```
cmd/apreg-server   registry server entrypoint
cmd/apreg          CLI entrypoint
internal/manifest  plugin.json / mcp.json parsing & validation
internal/schema    vendored official JSON Schemas (go:embed)
internal/pack      tar.gz pack/unpack
internal/store     storage interfaces (+ sqlite, fsblob implementations)
internal/registry  business logic — the only caller of internal/store
internal/api       REST handlers (HTTP only, calls internal/registry)
internal/web       read-only server-rendered browsing UI
internal/auth      password hashing, API token issuance/checks
```

`api` and `web` only ever call `registry`; `registry` only ever calls the
`store.Store`/`store.BlobStore` interfaces. Swapping SQLite for Postgres or
local disk for S3 later is a new file implementing those interfaces, not a
rewrite — see the package doc comment on `internal/store` for details.

## Tests

```bash
go test ./...
```
