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
| `caerus-framework-logs` | `log/slog` logging; components subscribe with `OnReconfigureFor`. |
| `caerus-framework-configuration` | Per-component config sources with validated hot-reload. |
| `caerus-framework-observability` | `/livez`, `/readyz`, `/metrics`, tracing. Binds in `Run`. |
| `caerus-framework-valkey` | Valkey/Redis component. |
| `caerus-framework-postgresql` | PostgreSQL component (pgx pool). |

More capabilities (HTTP, VPQ, Resend, …) live in other
`caerus-framework-*` modules. Core does not import them.

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

The four methods above are the **required** contract. Everything else is
optional: implement an interface only when you need that behavior. The
framework finds it with a type assert. There is no `RegisterRunnable`
call.

`GetDependencies` names are component `Name()` values (registry identity),
not configuration source names (`--postgresql`, `config/postgresql.json`).
Prefer matching those two strings in new modules so the names look the
same; they are still two concepts. If they differ, peers must depend on
`Name()`.

| If you need… | Implement (this module) | What it is *not* |
|---|---|---|
| Peers that must `Init` first | `Dependencies` | Not the config file’s source name |
| Children built in `New` (queues, refreshers) | `Subcomponents` | Not “call child `Init` / `Run` yourself” — the framework expands the tree |
| A worker or listen socket that occupies the process | `Runnable` | Not `Init`. Jobs never start Runnables, so a listen in `Init` would open a port during migrate |
| A config file / env / `--flag` the module owns | `ConfigSourceRegistrar` | Not something `main` registers. Logs and observability cannot import configuration (cycle): they use `CoreConfigSource` instead |
| Reconnect when that source reloads | `ConfigReloader` — `OnConfigReload(source string, cfg any)` | Not a fan-out to dependents. Store the **peer component** and call `Client()` / `Pool()` per use |
| A one-shot CLI task (`--postgresql.job=migrate`) | `JobRunner` (`RunJob(ctx, task)`) | Not a second process model. `Migrator` is only a fallback for the `"migrate"` task if you have no `JobRunner` |
| A voice on Kubernetes `/readyz` | `HealthProvider` | Not liveness (`/livez`). Not DegradedMode (that only answers “may `Initialize` finish without a live store?”) |

Types authors copy:

```go
type Dependencies           interface { GetDependencies() []string }
type Subcomponents          interface { Subcomponents() []CaerusComponent }
type Runnable               interface { Run(ctx context.Context) error }
type ConfigSourceRegistrar  interface { RegisterConfigSources(conf any) error }
type CoreConfigSource       interface { CoreConfigSource() ([]ConfigSourceValue, error) }
type ConfigReloader         interface { OnConfigReload(source string, cfg any) }
type JobRunner              interface { RunJob(ctx context.Context, task string) error }
type Migrator               interface { Migrate(ctx context.Context) error }
type HealthProvider         interface { Health(ctx context.Context) error }
```

`MetricsProvider` is **not** in this module. Observability defines it so
`/metrics` can scrape data clients. Implement it in the sibling that
exposes samples.

The configuration component (not your app) also implements `ConfigArgv`,
`JobSource`, and `ConfigSourceAdder` so the kernel can absorb argv and
register core sources without an import cycle. Do not implement those on
a product component.

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
   | `ObservabilityStage` | Kubernetes `/livez` `/readyz` `/metrics`, tracing (listen in `Run`) |
   | `SecretsStage` | **Reserved parking space.** Nothing occupies it today. See below. |

   **Credentials are not waiting on a vault component.** Production
   secrets are files Kubernetes (or Compose) mounts from a Secret /
   ConfigMap. The data module registers that file as its configuration
   source (`WithConfigSource`), `fsnotify` reloads it, and
   `ConfigReloader` builds a new client, pings, swaps, and keeps
   last-good on failure. That *is* the rotation plane. Process env is
   for local/`go run` and CI; env is not watchable, so do not treat
   env-injected Secrets as the primary credential story.

   ```text
   Wrong: leave a DSN only in POSTGRESQL_DSN and wait for SecretsStage /
          a KMS module to “do credentials properly.”
   Right: mount the Secret as a file, bind WithConfigSource, implement
          OnConfigReload, last-good if reconnect fails.
   ```

   `SecretsStage` stays in the bootstrap prefix so a future credential
   helper *could* Init before data. Do not invent a half vault in core
   to fill the slot. Do not read that empty stage as “KMS is coming
   next tag.”

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
mongo         (stage "data", depends on: "configuration")
    ↓ earlier stage (bootstrap)
configuration
```

That sketch does **not** use `SecretsStage`. A data module depends on
`configuration` (and usually `logs`) and reads credentials from **its
own** source file. There is no `secrets` component to list in
`GetDependencies` today.

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
share a stage; ties are broken by registration order. This is how the
occupied bootstrap stages order themselves: `logs`, `configuration`, and
`observability` live in that prefix, so a chain such as
`observability -> configuration -> logs` initializes in that order.
`SecretsStage` is in the prefix but empty — do not declare a dependency
on a `"secrets"` component unless you have registered one. A bootstrap
component never needs to reach outside the prefix: everything it needs
is in an earlier or the same bootstrap stage.

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
