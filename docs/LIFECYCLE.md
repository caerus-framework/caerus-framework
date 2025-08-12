# Caerus Framework — Component Lifecycle

This document describes the lifecycle guarantees provided by the core
`caerus-framework` module, and how to add a component.

## Lifecycle phases

### 1. Registration

Application stages are registered with `RegisterStage` in the order they should
initialize (the framework-owned bootstrap stages — logs, configuration,
observability, secrets — are already registered). Components are then
registered with `AddComponent` before the framework starts. Duplicate names,
nil components, and re-registered stages are rejected:

```go
fw := caerusframework.New()
if err := fw.RegisterStage("data"); err != nil { /* ... */ }
if err := fw.RegisterStage("serve"); err != nil { /* ... */ }

if err := fw.AddComponent(logsComp); err != nil { /* ... */ }
if err := fw.AddComponent(mongoComp); err != nil { /* ... */ }
```

### 2. Validation (fail-fast, before anything runs)

`Validate()` resolves the dependency graph and returns the initialization
order. It fails on unknown dependency names and on dependency cycles, with the
concrete cycle path in the error. It is cached (idempotent) and safe to call
before `Run`, e.g. from a test:

```go
func TestWiring(t *testing.T) {
    fw := buildFramework()
    if _, err := fw.Validate(); err != nil {
        t.Fatalf("component wiring is broken: %v", err)
    }
}
```

`Run`/`Initialize` validate automatically, and do so **before** any component
is initialized, so a broken graph never starts a resource.

### 3. Initialization

`Initialize(ctx)` calls `Init` on every component in resolved order:

- Components initialize stage by stage: every component in an earlier stage
  (`GetInitOrderStage`) runs before any component in a later stage. Stage order
  is the bootstrap prefix (logs → configuration → observability → secrets)
  followed by registered application stages in registration order.
- Within a stage, components initialize after every component named in their
  `GetDependencies()` (topological sort); ties break by registration order.
- A dependency may only reference a component in the same or an earlier stage.
  Referencing a later stage is a wiring error (see `Validate`).
- On **failure**, the already-initialized components are shut down in reverse
  order and the original error is returned. `Initialize` is idempotent after a
  successful run.

`Init` must return when the component is ready and honor `ctx`
cancellation/deadlines. Do not store `ctx` beyond the call; use `fw` to reach
other components:

```go
func (m *CFMongoDB) Init(ctx context.Context, fw *cf.CaerusFramework) error {
    m.fw = fw
    m.log = cf.MustGet[*cf_logs.Logs](fw).Logger()
    // connect, ping, then return
    return nil
}
```

### 4. Running

`Run(ctx)`:

1. Initializes all components (via `Initialize`).
2. Launches every component implementing `Runnable` in a goroutine.
3. Blocks until `ctx` is canceled **or** a runner returns an error.
4. Shuts everything down in reverse init order and returns.

A runner that returns an error cancels the framework context, so other runners
are asked to stop too:

```go
func (w *Worker) Run(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return nil // clean stop
        case job := <-w.jobs:
            if err := w.process(job); err != nil {
                return err // cancels the whole run
            }
        }
    }
}
```

A clean cancellation returns `nil` (runner `context.Canceled` errors are
normalized away).

### 5. Shutdown

`Shutdown(ctx)` stops every initialized component in **reverse init order**
(the reverse of step 3), so dependencies are torn down after their dependents.
It is idempotent and safe to call even if nothing was initialized. Prefer
`Run`'s automatic shutdown; call `Shutdown` explicitly only when using
`Initialize` separately.

## Guarantees summary

| Property | Guarantee |
|---|---|
| Init order | Stage by stage (stage order), then dependency-resolved topological sort within each stage |
| Stages | Bootstrap prefix (logs → configuration → observability → secrets) is fixed; application stages register in order and run after |
| Unregistered stage | Rejected by `Validate` before any `Init` |
| Cycles | Rejected by `Validate` before any `Init`; error shows the cycle path |
| Forward-stage dep | Rejected by `Validate` (a dep can never pull a later stage earlier) |
| Unknown dep | Rejected by `Validate` |
| Init failure | Already-initialized components are shut down in reverse order; error returned |
| Runner failure | Framework context canceled; full shutdown; error returned |
| Clean cancel | `Run` returns `nil` |
| Shutdown | Reverse init order; idempotent |
| Exits | The core never calls `os.Exit`; all failures are errors |

## Writing a component

A component lives in its own module (`github.com/caerus-framework/caerus-framework-<name>`)
and implements `CaerusComponent`. It declares the stage it belongs to, which
must have been registered with `RegisterStage`:

```go
const myStage = cf.Stage("my-stage") // registered via fw.RegisterStage(myStage)

type CFExample struct {
    fw *cf.CaerusFramework
    log *cf_logs.Logs
}

func (c *CFExample) Name() string { return "example" }

func (c *CFExample) GetInitOrderStage() cf.Stage {
	return myStage // see ARCHITECTURE.md for stages
}

func (c *CFExample) GetDependencies() []string {
	return []string{"logs", "configuration"} // must be registered names, in the same or an earlier stage
}

func (c *CFExample) Init(ctx context.Context, fw *cf.CaerusFramework) error {
    c.fw = fw
    c.log = cf.MustGet[*cf_logs.Logs](fw)
    return nil
}

func (c *CFExample) Shutdown(ctx context.Context) error { return nil }
```

Optional: implement `Runnable` for a background worker, `ConfigReloader` for
`OnConfigReload()` notifications from the configuration component.

See `component_example/caerus_example.go` for a compilable starting point.
