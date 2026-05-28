# Flagbase

An open-source, feature-driven Platform as a Service (PaaS) that treats feature flags, A/B testing, and observability as first-class citizens. Flagbase integrates identity-and-access management (IAM) directly with a real-time flag evaluation engine so developers can safely **test in production** by scoping experimental features to specific roles or sessions without affecting end-users.

## How It Works

Every incoming request flows through three tightly coupled subsystems:

```
[Incoming Request]
        │
        ▼
[Gateway Proxy]  ──→  extract JWT / session context
        │
        ▼
[IAM Evaluator]  ──→  resolve user + role ("developer")
        │
        ▼
[Feature Engine] ──→  match rule → toggle flag for this session only
        │
        ▼
[Backend / Function]
```

A developer's JWT carries a `role: developer` claim. When the Gateway intercepts their request, the Feature Engine evaluates flags against *that request's context* exclusively — production traffic is never affected.

## Architecture

Flagbase uses a **Hexagonal Architecture (Ports and Adapters)** with a dual-mode deployment model:

| | Local (default) | Cluster |
|---|---|---|
| **Storage** | SQLite (WAL mode) | PostgreSQL / DynamoDB |
| **Compute** | Wazero (WASM) | Firecracker MicroVMs |
| **Messaging** | Embedded NATS | NATS JetStream / Kafka |
| **Routing** | Chi reverse proxy | Envoy / Traefik |
| **Overhead** | Zero — single binary | Helm + cloud infra |

## Quick Start

### Prerequisites

- Go 1.21+
- [Task](https://taskfile.dev) (optional, for build automation)

### Run

```bash
# Build
task build

# Start with defaults (SQLite + embedded NATS on :8080)
./flagbase start

# Or pass a config file
./flagbase start --config config.yaml
```

### Configuration

Flagbase runs with sensible defaults and needs no config file for local use. Override with a YAML file:

```yaml
server:
  host: "0.0.0.0"
  port: 8080

database:
  path: "flagbase.db"

iam:
  jwt_secret: "change-me-in-production"
  token_ttl: 24h

storage:
  base_path: "./data/storage"

events:
  nats_port: 4222
```

## API Reference

### Auth

```bash
# Register
POST /auth/register
{"email": "dev@example.com", "password": "secret", "role": "developer"}

# Login — returns a Bearer token
POST /auth/login
{"email": "dev@example.com", "password": "secret"}
```

### Feature Flags

All flag endpoints require a valid Bearer token.

```bash
GET    /api/v1/flags              # list all flags
POST   /api/v1/flags              # create a flag
GET    /api/v1/flags/{key}        # get a flag
PUT    /api/v1/flags/{key}        # update a flag
DELETE /api/v1/flags/{key}        # delete a flag
GET    /api/v1/flags/{key}/evaluate  # evaluate flag for the caller's identity
```

**Create flag example:**

```json
{
  "key": "new-checkout-ui",
  "name": "New Checkout UI",
  "enabled": true,
  "default_value": false,
  "rules": [
    {
      "attribute": "role",
      "operator": "equals",
      "value": "developer",
      "variant": true,
      "priority": 0
    }
  ]
}
```

Rule operators: `equals`, `not_equals`, `contains`, `in` (comma-separated list).

### Gateway

```
GET|POST /gateway/{path}
```

Routes registered against a flag key are only proxied when the flag evaluates to `true` for the caller's identity. Register routes programmatically via `gateway.ProxyHandler.RegisterRoute`.

### Health

```
GET /health  →  {"status": "ok"}
```

## Go SDK

```go
import "github.com/mtfuller/flagbase/pkg/sdk"

client := sdk.NewClient("http://localhost:8080", bearerToken)

// Evaluate a flag for the authenticated user
enabled, err := client.EvaluateFlag("new-checkout-ui")

// Record a metric event (used by the anomaly-detection worker)
err = client.RecordEvent("new-checkout-ui", "control", "error", 1)
```

## Observability & Anomaly Detection

A background worker polls metrics every 30 seconds. Any flag that accumulates more than 10 `error` events within a 5-minute window is **automatically disabled** and a `flagbase.flag.disabled` event is published to the NATS bus. This prevents bad rollouts from reaching production traffic before a human can react.

## Project Structure

```
.
├── cmd/                      # Cobra CLI commands
│   ├── root.go               # Root command + global flags
│   ├── start.go              # "start" — wires and launches all services
│   └── version.go            # "version" — prints build metadata
├── internal/
│   ├── api/                  # HTTP server, routes, handlers, middleware
│   ├── color/                # ANSI terminal output helpers
│   ├── config/               # YAML config loading with defaults
│   ├── database/             # SQLite connection + schema migrations
│   ├── event/                # Embedded NATS server + client wrapper
│   ├── feature/              # In-memory flag evaluation engine (Engine + Flag + Rule)
│   ├── function/             # Wazero WASM runtime (sandboxed compute)
│   ├── gateway/              # Context-aware reverse proxy (ProxyHandler)
│   ├── iam/                  # JWT auth, user registration/login, claims
│   ├── logger/               # Structured leveled logger
│   ├── spinner/              # Terminal spinner for long-running ops
│   ├── storage/              # BucketAdapter interface + local filesystem impl
│   ├── version/              # Build metadata (version, commit, date)
│   └── worker/               # Anomaly detection + metric aggregation goroutine
├── pkg/
│   ├── example/              # Example business-logic package
│   └── sdk/                  # Go HTTP client for the flagbase API
├── tests/
│   └── integration_test.go   # End-to-end CLI tests
├── main.go                   # Entry point
├── Taskfile.yml              # Build / test automation
└── go.mod
```

## Development

```bash
task build            # compile ./flagbase
task run              # go run main.go start
task test             # unit + integration tests
task test-unit        # internal/ and pkg/ packages only
task test-integration # tests/ only
task coverage         # coverage.html report
task tidy             # go mod tidy
task clean            # remove binary
```

## Adding a Feature Flag Rule

Rules are evaluated in `priority` order (lowest first). The first matching rule wins; if no rule matches, `default_value` is returned.

Supported operators:

| Operator | Behavior |
|---|---|
| `equals` | case-insensitive exact match |
| `not_equals` | case-insensitive inverse match |
| `contains` | substring match |
| `in` | match any value in comma-separated list |

## Adding a New Command

Create `cmd/<name>.go`:

```go
package cmd

import (
    "github.com/mtfuller/flagbase/internal/color"
    "github.com/spf13/cobra"
)

var myCmd = &cobra.Command{
    Use:   "mycommand",
    Short: "One-line description",
    RunE: func(cmd *cobra.Command, args []string) error {
        color.Success("done")
        return nil
    },
}

func init() {
    rootCmd.AddCommand(myCmd)
}
```

## License

MIT — see [LICENSE](LICENSE).
