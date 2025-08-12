# Caerus Framework — Architecture

Caerus is a minimal, component-based application framework. The core module
(`caerus-framework`) owns only the **component contract** and the
**lifecycle**: registration, dependency resolution, initialization, running and
shutdown. Concrete capabilities (logging, configuration, MongoDB, gRPC, ...)
live in separate modules under `github.com/caerus-framework/caerus-framework-*`.

This split mirrors the original `go-panframe` idea — an opinionated
integration layer with uniform component handling — but fixes the structural
weaknesses: no init ordering, no error propagation, no shutdown, and a
reflection-typed registry that panicked.

## Modules

| Module | Purpose |
|---|---|
| `caerus-framework` | Core. Component contract + lifecycle + dependency graph. |
| `caerus-framework-logs` | `log/slog`-based logging with stack-trace support. |
| `caerus-framework-configuration` | Per-component config sources with validated hot-reload. |
| `caerus-framework-mongodb` | MongoDB component (multiple clients/databases). |
| `caerus-framework-clickhouse` | ClickHouse component. |
| `caerus-framework-valkey` | Valkey/Redis component. |
| `caerus-framework-postgresql` | PostgreSQL component (pgx pool). |
| `caerus-framework-grpc` | *(future)* gRPC servers/clients. |

`go-panframe` is retained in this workspace as an archive/reference and is not
modified.

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
mongo, ok := caerusframework.Get[*cf_mongodb.CFMongoDB](fw) // or MustGet
```

`Get`/`MustGet` replace the old reflection-keyed `GetIngridient(&X{}).(*X)`
accessor: no stringly-typed reflect keys, no nil-dereference panic on a missing
component, and the returned value is already the concrete pointer the component
module exports.

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

   Application stages are **developer-defined**: register them with
   `RegisterStage` in the order they should initialize, and they run after the
   bootstrap prefix. Components declare their stage via `GetInitOrderStage`;
   declaring a stage that was never registered is a wiring error. A component
   whose stage is unregistered fails `Validate` before any `Init`.

   ```go
   fw := caerusframework.New() // logs, configuration, observability, secrets already registered

   if err := fw.RegisterStage("data"); err != nil { /* ... */ }     // runs 5th
   if err := fw.RegisterStage("serve"); err != nil { /* ... */ }    // runs 6th
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

A future static analyzer (e.g. a `go vet`-style checker) could additionally
validate cross-package dependency declarations, but the runtime check remains
the source of truth.

## Error policy

- `Init`/`Shutdown`/`Run` return errors. Nothing calls `os.Exit`.
- A failed `Init` triggers shutdown of the already-initialized components in
  reverse order, then returns the error.
- A failing `Runnable` cancels the framework context and triggers full
  shutdown.
- A clean cancellation returns `nil`.
