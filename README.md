# caerus-framework

[![CI](https://github.com/caerus-framework/caerus-framework/actions/workflows/ci.yml/badge.svg)](https://github.com/caerus-framework/caerus-framework/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/caerus-framework/caerus-framework/graph/badge.svg)](https://codecov.io/gh/caerus-framework/caerus-framework)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)


Caerus-Framework is a small Go **lifecycle framework/platform** for teams that run many Kubernetes
services and want one shared way to wire `main`: components, dependencies,
Init/Shutdown, and `RunWithSignals`
— not a DI container
- not an ORM
- not a job platform

The core owns the component contract and lifecycle only; capabilities (logs,
configuration, observability, postgres, valkey, ...) live in sibling `caerus-framework-*`
modules. For a runnable golden path, see
[`caerus-framework-demoapp`](https://github.com/caerus-framework/caerus-framework-demoapp).

## Features

- Component lifecycle: `Init` / `Shutdown` with `context.Context` and error
  propagation (no `os.Exit` anywhere).
- **Dependency-driven initialization**: components declare dependencies by
  name; the framework topologically sorts the graph and initializes required
  components first.
- **Cycle detection**: cyclic dependencies and unknown dependency names are
  rejected by `Validate()` before anything runs — check it from a test to fail
  CI, and it always fails fast at startup.
- Deterministic ordering (registered stages — fixed bootstrap prefix, then
  application stages in registration order — with dependency topo sort within a
  stage), reverse shutdown, and safe partial-failure teardown.
- Typed component access via `Get` / `MustGet` (no reflection-keyed lookups).
- Background workers via the optional `Runnable` interface.
- Production entrypoint `RunWithSignals` (SIGINT/SIGTERM, optional init/shutdown
  timeouts). Prefer this over bare `Run` in services.
- Panic recovery around `Init` / runners / `Shutdown`; serialized `Initialize`.
- Parallel `Init` within topological waves (same-stage independents concurrently).
- **Self-sufficient components**: optional `ConfigSourceRegistrar` lets components
  register their own configuration source; the framework owns argv entirely
  (registrar pass → `ParseFlags`) before `Initialize`/`Run`. Flag overlay is
  interspersed (GNU-style), and unknown flags + positional args — the app's
  subcommand and its arguments — come back via `LeftoverArgs()` (`AbsorbArgs()`
  for early access). `main` touches `os.Args` nowhere.
- **Job-only migrate path**: the framework asks configuration (via `cf.JobSource`)
  whether any module-declared **job flag** is set (e.g. postgresql's
  `--postgresql.job=migrate` — the flag names the instance, the value names the
  task); if so it runs core + the named target component's `RunJob` (Migrator
  accepted for the `migrate` task) then exits. `fw.Migrate(ctx, target)` is the
  explicit sugar for multi-tool binaries.
- Optional `FrameworkOptions.Args` for binaries that must strip a binary-level
  prefix before absorption (default `os.Args[1:]`).

## Quick start

`main` declares **chassis + app**. Logs, configuration, and observability are
always-on core (seeded via `FrameworkOptions`). Components own their config
sources; the framework absorbs argv. Prefer `RunWithSignals` in services.

```go
package main

import (
	"context"
	"log"
	"time"

	cf "github.com/caerus-framework/caerus-framework"
	cf_postgres "github.com/caerus-framework/caerus-framework-postgresql"
	cf_valkey "github.com/caerus-framework/caerus-framework-valkey"

	"example.com/myapp/internal/app"
)

func main() {
	fw := cf.New(&cf.FrameworkOptions{
		Logs: &cf.LogsSettings{
			Format:       "json",
			Level:        "info",
			ConfigSource: "logs",
		},
		Observability: &cf.ObservabilitySettings{
			Address:      ":9090", // /livez, /readyz, /metrics
			ConfigSource: "observability",
		},
		Components: []cf.CaerusComponent{
			cf_postgres.New(
				cf_postgres.WithConfigSource("postgresql", "config/postgresql.json"),
				// WithEmbeddedMigrations(embedFS, "migrations") when you ship SQL.
				// Local only: WithMigrateOnInit(). Prod: `--postgresql.job=migrate` Job.
			),
			cf_valkey.New(
				cf_valkey.WithConfigSource("valkey", "config/valkey.json"),
			),
			app.New(app.Options{}), // product HTTP / Subcomponents live here
		},
	})

	if err := fw.RunWithSignals(context.Background(),
		cf.WithShutdownTimeout(15*time.Second),
	); err != nil {
		log.Fatal(err)
	}
}
```

Process shapes are **job flags**, not subcommands — e.g.
`myapp --postgresql.job=migrate` initializes only that component’s dependency
closure, runs the task, exits (no Runnables).

Full golden path (Compose, seed, interest VPQ, catalog-summary):
[`caerus-framework-demoapp`](https://github.com/caerus-framework/caerus-framework-demoapp).

`AddComponent` / bare `cf.New()` without options remain valid for tests and
embedded use; production services should follow the shape above.

## Docs

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — component model, dependency
  graph, ordering, error policy.
- [docs/LIFECYCLE.md](docs/LIFECYCLE.md) — lifecycle guarantees and how to
  write a component.
- [`component_example/`](component_example/) — compilable example component.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
