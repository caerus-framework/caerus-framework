# caerus-framework

Caerus Framework — core.

A minimal, component-based application framework. The core owns the component
contract and lifecycle only; capabilities (logs, configuration, mongo, ...)
live in sibling `caerus-framework-*` modules.

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

## Quick start

```go
fw := caerusframework.New()
fw.AddComponent(logsComp)   // e.g. caerus-framework-logs
fw.AddComponent(mongoComp)  // e.g. caerus-framework-mongodb, depends on "logs"
fw.AddComponent(appComp)    // depends on "logs", "mongodb"

if _, err := fw.Validate(); err != nil { /* wiring broken */ }
if err := fw.Run(ctx); err != nil { /* app failed */ }
```

## Docs

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — component model, dependency
  graph, ordering, error policy.
- [docs/LIFECYCLE.md](docs/LIFECYCLE.md) — lifecycle guarantees and how to
  write a component.
- [`component_example/`](component_example/) — compilable example component.
