# Security policy

This file is for the **`caerus-framework`** Go module — the lifecycle kernel
(`New`, `AddComponent`, `Initialize`, `Run` / `RunWithSignals`, jobs such as
`--postgresql.job=migrate`). It is **not** a sandbox between components, and
it is **not** the place that parses config files, opens databases, or serves
HTTP.

Sibling modules (`caerus-framework-configuration`, `caerus-framework-logs`,
`caerus-framework-observability`, `caerus-framework-http`,
`caerus-framework-postgresql`, `caerus-framework-valkey`, …) own most
credential and network risk. Report a bug against **the module that reads
the secret or opens the socket**. If you are unsure, report it here and we
will move it.

## How to report a vulnerability

**Use GitHub’s private vulnerability reporting on this repository.** Do not
open a public issue, pull request, or discussion for an unfixed security
problem.

1. Open [Report a vulnerability](https://github.com/caerus-framework/caerus-framework/security/advisories/new)
   on `caerus-framework/caerus-framework`.
2. Describe the issue, the affected module (core vs a sibling), versions or
   tags, and a way we can reproduce it. You do **not** need a full exploit.
3. Wait for a maintainer reply in that advisory. We will say whether we
   agree it is a vulnerability, which module owns the fix, and when we
   expect to ship a tag.

There is no separate security email. The advisory form is the only reporting
path.

This project is pre-1.0 (`v0.0.x`). We do not promise a fixed response-time
SLA. We do treat private reports as confidential until a fix is tagged (or
until we explain why we do not consider it a vulnerability).

## Supported versions

Only the **latest SemVer tag on the default branch** of this repository is
supported for security fixes. Older `v0.0.x` tags are not patched; upgrade
to the latest tag.

| Version | Supported |
|---|---|
| Latest tag on `main` | Yes |
| Older `v0.0.x` tags | No |

Fixes land on the default branch and ship in the next tag. We do not
backport to older pre-1.0 tags.

If the bug is in a sibling module, look at **that** module’s latest tag, not
this kernel’s.

## What this kernel does and does not do

Core accepts:

- **argv** — handed to the configuration component (`ParseFlags`); core does
  not parse flags itself.
- **Unix signals** — `RunWithSignals` watches SIGINT/SIGTERM (overridable).
- **in-process API** — `New` / `AddComponent` / `Initialize` / `Run*` /
  `Migrate` / `Get*` from trusted `main` and registered components.

Core does **not** read environment variables or config files, open network
listeners, spawn subprocesses, or call `os.Exit`. Listeners belong in a
component’s `Run` (jobs never start `Runnable`s). Credentials belong in
module config (prefer mounted files that reload), not in this kernel.

## Trust model (please read before filing)

Every registered component receives `*CaerusFramework` at `Init` and can
look up peers (`Get`, `GetByName`, `Components()`). A malicious or buggy
component is **full-process compromise by design**. That is the same class
as other wiring / DI frameworks: the kernel orders Init and Shutdown; it
does not isolate one chassis from another.

The following are **not** vulnerabilities in core:

- Component A can read component B’s client, pool, or config through the
  framework API.
- A panic or Init error string that includes a value the **component** put
  there (DSNs and secrets must not be interpolated by the module that owns
  them; core will not redact for you).
- `caerusvet` missing a dependency that cannot be resolved statically (the
  checker prefers false negatives; `Validate` at startup is the runtime
  check).
- Observability `/metrics` reachable on a **serving** process (that is the
  observability module plus your NetworkPolicy). A **job** process
  (`--….job=…`) must not start listeners; if one does, report it against
  the module that called `Listen` in `Init`.

## After a fix

We prefer a normal patch tag and release notes over silent changes. GitHub
Security Advisories may be published once a fix is available. Credit is
offered to reporters who want it.
