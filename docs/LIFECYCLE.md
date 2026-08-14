# Caerus Framework — Component Lifecycle

This document describes the lifecycle guarantees provided by the core
`caerus-framework` module, and how to add a component.

## Lifecycle phases

### 1. Registration

Stages are auto-registered by `AddComponent`: a component's
`GetInitOrderStage` stage is registered the first time a component declares it,
in first-seen order after the bootstrap prefix (logs, configuration,
observability, secrets — always registered first). There is no explicit stage
API. Components are then registered with `AddComponent` before the framework
starts. Duplicate names, nil components, and empty stages are rejected:

```go
fw := caerusframework.New()
if err := fw.AddComponent(dbComp); err != nil { /* ... */ }   // "data" stage auto-registered
if err := fw.AddComponent(webComp); err != nil { /* ... */ }  // "serve" stage auto-registered after "data"
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
- Concurrent `Initialize` calls are serialized; a second caller waits and then
  observes the already-started state (no double-init).
- Panics in `Init` are recovered and returned as errors (partial teardown still
  runs).
- **Parallel Init within a stage:** components with no unmet same-stage
  dependencies on each other form a wave and call `Init` concurrently. The next
  wave starts only after the previous wave finishes. Stages remain strictly
  ordered. On failure, in-flight peers in the wave are canceled (via ctx) and
  every successfully initialized component is shut down in reverse order.

`Init` must return when the component is ready and honor `ctx`
cancellation/deadlines. Do not store `ctx` beyond the call; use `fw` to reach
other components. **Do not bind a listen socket in `Init`.** Jobs initialize a
subset of the graph and never start `Runnable`s, so a listen here would open a
port during `--postgresql.job=migrate`. Implement `Runnable` and bind in `Run`
(the same split `caerus-framework-http` and observability use).

```go
func (m *CFMongoDB) Init(ctx context.Context, fw *cf.CaerusFramework) error {
    m.fw = fw
    m.log = cf.MustGet[*cf_logs.Logs](fw).Logger()
    // connect, ping, then return
    return nil
}
```

### 4. Running

`Run(ctx)` (tests / embedded cancellation):

1. Initializes all components (via `Initialize`).
2. Launches every component implementing `Runnable` in a goroutine.
   HTTP listeners, queue workers, and other “occupy the process until
   cancel” work belong here — not in `Init`.
3. Blocks until `ctx` is canceled **or** a runner returns an error.
4. Shuts everything down in reverse init order and returns. If `ctx` is already
   canceled, Shutdown uses `context.Background()` so teardown is not starved.

`RunWithSignals(ctx, opts...)` is the production entrypoint: same lifecycle,
but stops on `SIGINT`/`SIGTERM` (override with `WithSignals`) and supports
`WithInitTimeout` / `WithShutdownTimeout`. Shutdown after a signal uses a fresh
background context so the canceled signal context cannot abort teardown.

A runner that returns an error cancels the framework context, so other runners
are asked to stop too. Concurrent `Run` / `RunWithSignals` calls are rejected.
Panics in runners are recovered, cancel peers, and trigger shutdown:

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

### 4b. Job-only path (no Runnables)

`RunWithSignals` asks configuration whether a module job flag was set
(for example `--postgresql.job=migrate`). If so the framework does **not**
serve:

1. Initializes the bootstrap stages plus the target’s dependency closure.
2. Runs the named task (`JobRunner.RunJob`, or `Migrator.Migrate` for
   `"migrate"`).
3. Shuts down and returns. **No `Runnable` is started.**

That is why listeners must bind in `Run`. Observability still *initializes*
on a migrate Job (it is a bootstrap stage) but does not open `:9090`. The
same is true of `caerus-framework-http` (app stage: usually not even in the
migrate closure; even if it were, it would not listen).

**One job per target per process.** Two flags (or two `JobRequest`s) that
name the same component `Name()` are a hard error before any data Init —
even if both ask for the same task. Distinct targets in one argv are
allowed (two named postgres instances, two `--….job=migrate` flags).

**One entrypoint per process.** After a job has begun Init, this framework
instance is spent: `Initialize`, `Run`, `RunWithSignals` (serve path), and
a second `RunJob` / `Migrate` all fail, including after `Shutdown`.
Construct a new framework (or, in production, exit — the K8s Job is done).

`Validate` / wave resolution always see the **whole** registered graph. A
cycle or unknown dependency on a sibling the job will not Init still fails
the Job. That is fail-fast wiring, not a migrate bug. Do not leave broken
components registered “because the job would not touch them.”

`fw.Migrate(ctx, target)` / `fw.RunJob(ctx, target, task)` are the same
machine for multi-tool binaries.

### 5. Shutdown

`Shutdown(ctx)` stops every initialized component in **reverse init order**
(the reverse of step 3), so dependencies are torn down after their dependents.
It is idempotent and safe to call even if nothing was initialized. Prefer
`Run`'s automatic shutdown; call `Shutdown` explicitly only when using
`Initialize` separately.

**Do not call `Shutdown` while `Run` is in progress.** That call is refused
(`cannot Shutdown while Run is in progress`). Cancel the `Run` context, or
send SIGINT/SIGTERM to `RunWithSignals`; those paths drain runners and then
`Shutdown` themselves. An extra `Shutdown` from another goroutine would tear
postgres/valkey/HTTP out from under live `Runnable`s.

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
| Shutdown | Reverse init order; idempotent; **refused while Run is in progress** |
| Jobs skip Runnables | Listeners that bind in `Run` stay closed during migrate/seed |
| One job per target | Two flags naming the same component `Name()` fail before Init |
| One entrypoint per process | After a job, Initialize / Run / a second job on this instance fail |
| Whole-graph Validate | A broken unused sibling still fails a Job (wiring error) |

## Writing a component

A component lives in its own module (`github.com/caerus-framework/caerus-framework-<name>`)
and implements `CaerusComponent`. It declares the stage it belongs to via
`GetInitOrderStage`; `AddComponent` registers that stage automatically, so the
component needs no extra registration call:

```go
const myStage = cf.Stage("my-stage") // auto-registered on first AddComponent

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
    c.log = cf.MustGet[*cf_logs.Logs](fw) // recovered if this panics in Init
    return nil
}

func (c *CFExample) Shutdown(ctx context.Context) error { return nil }
```

`MustGet` panics if the peer is missing. The framework recovers panics in
`Init`, `Run`, and `Shutdown` only. Use it in `Init` and tests; do not call
it from an arbitrary goroutine (prefer `Get` and handle `ok`).

Optional: implement `Runnable` for a background worker **or HTTP listener**
(bind in `Run`, never `Init`), `ConfigReloader` for `OnConfigReload(source
string, cfg any)` notifications from the configuration component.

See `component_example/caerus_example.go` for a compilable starting point.
