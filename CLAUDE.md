# Flagbase — AI Agent Guide

## What This Project Is

Flagbase is an open-source, single-binary PaaS that bakes feature flags, IAM, and an anomaly-detection worker directly into one Go process. The key differentiator is the **Gateway → IAM → Feature Engine** pipeline: every HTTP request carries a JWT, and flag rules are evaluated against the caller's identity claims, enabling safe per-role testing in production.

## Build & Run

```bash
task build            # produces ./flagbase
task run              # go run main.go start (defaults: SQLite + NATS on :8080)
task test             # all tests (unit + integration)
task test-unit        # go test ./internal/... ./pkg/...
task test-integration # go test ./tests/...
task coverage         # coverage.html
task tidy             # go mod tidy
task clean            # remove binary
```

Requires: Go 1.21+, CGO enabled (sqlite3), [Task](https://taskfile.dev) (optional).

## Package Map

| Path | Role |
|---|---|
| `cmd/` | Cobra CLI entry points. Each file = one command. `start.go` wires everything. |
| `internal/api/` | Chi HTTP server, routes, handlers (`handlers.go`), middleware (`middleware.go`) |
| `internal/config/` | YAML config loader with `defaults()` fallback — no config file needed locally |
| `internal/database/` | SQLite connection (WAL mode) + embedded schema migration in `schema` const |
| `internal/event/` | Embedded NATS server + `Bus` wrapper (Publish / Subscribe) |
| `internal/feature/` | Flag evaluation engine. `Engine` caches flags in memory; mutations write to SQLite then reload |
| `internal/function/` | Wazero WASM runtime. Each `Invoke` call is isolated with a hard timeout |
| `internal/gateway/` | `ProxyHandler`: reverse proxy that evaluates a flag before routing |
| `internal/iam/` | JWT auth (`Service`), user CRUD, `Claims` struct, `UserContextKey` for middleware injection |
| `internal/storage/` | `BucketAdapter` interface + `LocalAdapter` (local filesystem). Add adapters here for S3 etc. |
| `internal/worker/` | Background goroutine: anomaly detection (auto-disables flags with >10 errors/5 min) |
| `pkg/sdk/` | Go HTTP client for the REST API; embed in services that need flag evaluation |

## Key Data Flow

1. **Request arrives** at `internal/gateway/ProxyHandler.ServeHTTP`
2. `IAMContextMiddleware` (`internal/api/middleware.go`) validates the Bearer token and injects `*iam.Claims` into `r.Context()`
3. `ProxyHandler` reads claims, builds an `evalCtx` map (`userId`, `role`, …)
4. `feature.Engine.EvaluateBool(flagKey, evalCtx)` walks rules in priority order and returns true/false
5. If true, the request is proxied to the backend URL via `httputil.ReverseProxy`

## Database Schema (SQLite)

Four tables, all created by `database.Migrate`:

- `users` — email, hashed password (SHA-256), role, tenant_id
- `feature_flags` — key (unique), name, description, enabled, default_value
- `flag_rules` — FK → feature_flags(key), attribute, operator, value, variant, priority
- `metrics` — flag_key, variant, event_type, value, recorded_at (used by anomaly worker)

## REST API Summary

```
GET  /health
POST /auth/register   {"email","password","role"}
POST /auth/login      {"email","password"}  →  {"token":"..."}

# All below require: Authorization: Bearer <token>
GET    /api/v1/flags
POST   /api/v1/flags            body: Flag JSON (see feature.Flag)
GET    /api/v1/flags/{key}
PUT    /api/v1/flags/{key}
DELETE /api/v1/flags/{key}
GET    /api/v1/flags/{key}/evaluate   evaluates for the caller's identity

GET|POST /gateway/*             routed if flag evaluates true
```

## Core Types

```go
// internal/feature/flag.go
type Flag struct {
    ID           string
    Key          string    // unique, used as map key and URL param
    Name         string
    Description  string
    Enabled      bool
    DefaultValue bool
    Rules        []Rule
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type Rule struct {
    ID        string
    FlagKey   string
    Attribute string   // context key, e.g. "role"
    Operator  string   // "equals" | "not_equals" | "contains" | "in"
    Value     string
    Variant   bool     // the value returned when this rule matches
    Priority  int      // lower = evaluated first
}

// internal/iam/claims.go
type Claims struct {
    UserID   string
    Email    string
    TenantID string
    Role     string
    Groups   []string
    jwt.RegisteredClaims
}
```

## Conventions

- **Errors:** always wrap with `fmt.Errorf("context: %w", err)`; use `RunE` in Cobra commands
- **Logging:** `internal/logger` — `logger.Info/Warn/Error/Debug`; user-facing output via `internal/color`
- **New command:** create `cmd/<name>.go`, define `cobra.Command`, register in `init()` with `rootCmd.AddCommand`
- **New internal package:** `internal/<pkg>/<pkg>.go` + `<pkg>_test.go` (table-driven, testify)
- **New storage adapter:** implement `storage.BucketAdapter`, add factory to `cmd/start.go`
- **Flag rules are priority-ordered:** lowest `priority` value wins; first match short-circuits
- **Thread safety:** `feature.Engine` uses `sync.RWMutex`; reload happens inside a write lock after every mutation

## Testing

```bash
# Unit — same package, white-box access
go test ./internal/... ./pkg/... -v

# Integration — full CLI via os/exec
go test ./tests/... -v

# Race detector
go test -race ./...

# Coverage target: >80% overall, >85% for critical paths
```

Test files live next to source (`internal/iam/service_test.go`). Integration tests call the real binary via `go run ../main.go`.

## Common Tasks

**Add a feature flag via curl:**
```bash
TOKEN=$(curl -s -X POST localhost:8080/auth/login \
  -d '{"email":"dev@example.com","password":"secret"}' | jq -r .token)

curl -X POST localhost:8080/api/v1/flags \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "key": "my-flag",
    "name": "My Flag",
    "enabled": true,
    "default_value": false,
    "rules": [{"attribute":"role","operator":"equals","value":"developer","variant":true,"priority":0}]
  }'
```

**Register a gateway route programmatically:**
```go
proxy.RegisterRoute(&gateway.Route{
    ID:         "r1",
    Pattern:    "/v2/checkout",
    BackendURL: "http://new-service:3000",
    FlagKey:    "new-checkout-ui", // only routes when flag == true for caller
})
```

**Record a metric (triggers anomaly detection):**
```go
worker.RecordMetric("my-flag", "experimental", "error", 1)
```

## What Not to Change Without Strong Reason

- `database.schema` — migrations are additive; altering existing columns breaks existing data
- `iam.UserContextKey` — the middleware and handlers share this context key; changing it silently breaks auth
- `feature.Engine` reload strategy — mutations must write then reload to keep cache consistent
- Port and adapter interfaces (`storage.BucketAdapter`) — downstream adapters depend on these contracts
