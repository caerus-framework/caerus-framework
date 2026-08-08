# Caerus Framework — Architecture

Caerus is a minimal, component-based application framework. The core module
(`caerus-framework`) owns only the **component contract** and the
**lifecycle**: registration, dependency resolution, initialization, running and
shutdown. Concrete capabilities (logging, configuration, observability, postgresql, ...)
live in separate modules under `github.com/caerus-framework/caerus-framework-*`.


## Modules

| Module | Purpose |
|---|---|
| `caerus-framework` | Core. Component contract + lifecycle + dependency graph. |
| `caerus-framework-logs` | `log/slog`-based logging with stack-trace support. |
| `caerus-framework-configuration` | Per-component config sources with validated hot-reload. |
| `caerus-framework-valkey` | Valkey/Redis component. |
| `caerus-framework-postgresql` | PostgreSQL component (pgx pool). |

## Component contract

Every component implements `CaerusComponent`:

```go
type CaerusComponent interface {
    Name() string
    GetInitOrderStage() Stage
    Init(ctx context.Context, fw *CaerusFramework) error
    Shutdown(ctx context.Context) error
}
```

Three optional interfaces extend the contract:

- `Dependencies` — declares component names that must be initialized first.
- `Runnable` — launches a background worker goroutine after initialization.
- `ConfigReloader` — receives `OnConfigReload()` after a validated config swap.

```go
type Dependencies interface { GetDependencies() []string }
type Runnable       interface { Run(ctx context.Context) error }
type ConfigReloader interface { OnConfigReload() }
```

## Component naming

`Name()` must be stable and unique across the framework. Names are the
identifiers used in dependency declarations. Components are registered as their
concrete pointer types and retrieved by type:

```go
pg, ok := caerusframework.Get[*cf_postgres.CFPostgres](fw) // or MustGet / GetByName
```

`Get` / `GetByName` / `MustGet` are typed lookups: no stringly reflect keys, no
nil-dereference panic on a missing component, and the returned value is already
the concrete pointer the component module exports.

When a component must be reached by its declared `Name()` rather than its Go
type — e.g. the configuration component resolving the owner of a config source
— use the name lookup `fw.Component(name) (CaerusComponent, bool)`.

## Dependency graph & ordering

Ordering is a two-level model:

1. **Stages** are an ordered list, owned by the framework. Every component in
   an earlier stage initializes before any component in a later stage, and
   shutdown happens in reverse. The framework owns a **bootstrap prefix** that
   is always registered first, in this fixed order:

   | Stage | Covers |
   |---|---|
   | `LogsStage` | logging bootstrap (slog + stack traces) |
   | `ConfigurationStage` | per-component config sources |
   | `ObservabilityStage` | Kubernetes health-check endpoints, metrics, tracing |
   | `SecretsStage` | KMS, credentials, mTLS material |

   Application stages are **developer-defined**: components declare their stage
   via `GetInitOrderStage`, and `AddComponent` registers it automatically the
   first time it is seen, in first-seen order, after the bootstrap prefix.
   There is no explicit stage API: a component is ordered by its declared stage
   relative to the other components actually added.

   ```go
   fw := caerusframework.New() // logs, configuration, observability, secrets already registered
   fw.AddComponent(dbComp)  // "data" stage auto-registered here, after the bootstrap prefix
   fw.AddComponent(webComp) // "serve" stage auto-registered after "data"
   ```

2. **Dependencies** are resolved **within a stage** by topological sort
   (Kahn's algorithm). A dependency may only reference a component in the
   *same or an earlier* stage:

```
app-core      (stage "business", depends on: "mongo", "kafka")
    ↓ same stage                                      ↓ earlier stage ("data")
mongo         (stage "data", depends on: "secrets")   satisfied by stage order
    ↓ earlier stage (bootstrap)
secrets
```

Declaring a dependency on a **later stage** is a wiring error — a dependency
can never pull a later-stage component earlier. This is validated before any
`Init` runs, together with two other wiring errors:

- **Unknown dependency** — a `GetDependencies()` name that no registered
  component provides.
- **Unregistered stage** — a component's `GetInitOrderStage()` names a stage
  that was never registered.
- **Cyclic dependency** — since forward stage references are rejected, a cycle
  can only exist within a single stage. The error includes a concrete cycle
  path, e.g. `caerus: cyclic component dependency detected: a -> b -> a`.

Same-stage dependencies give fine-grained ordering when several components
share a stage; ties are broken by registration order. This is exactly how the
bootstrap components order themselves: even though `logs`, `configuration` and
`secrets` all live in bootstrap stages, a `secrets -> configuration -> logs`
dependency chain initializes them in that order. A bootstrap component never
needs to reach outside its own stage — everything it needs is in an earlier or
the same bootstrap stage and is ordered by dependency.

See [LIFECYCLE.md](LIFECYCLE.md) for the full lifecycle description and the
guarantees around initialization failure and shutdown.

## Why not catch cycles at compile time?

The dependency graph is assembled at runtime from component instances in
separate Go modules — the compiler cannot see it. Cycles are therefore caught
at the earliest safe point:

1. **`Validate()`** — callable from a unit test, so wiring bugs fail CI (and
   `go test`) rather than production. This is the recommended "build-time"
   gate.
2. **Framework startup** — `Run`/`Initialize` resolve the graph *before* any
   component touches a resource, so a cycle never leaves an app half-started.

A `go vet`-style checker — **`caerusvet`** (`go tool caerusvet ./...` from the
core module or any dependent whose go.mod declares the `tool` directive) —
additionally catches Init peer lookups that are missing from
`GetDependencies` (literals / known `ComponentName` consts). It deliberately
prefers false negatives over false positives and does not replace runtime
`Validate` for the assembled graph. The analyzer lives at
[`cmd/caerusvet`](../cmd/caerusvet).

## Error policy

- `Init`/`Shutdown`/`Run` return errors. Nothing calls `os.Exit`.
- A failed `Init` triggers shutdown of the already-initialized components in
  reverse order, then returns the error.
- A failing `Runnable` cancels the framework context and triggers full
  shutdown.
- A clean cancellation returns `nil`.
