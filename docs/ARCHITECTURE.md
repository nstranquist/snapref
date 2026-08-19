# Architecture

SnapRef is a local-first browser automation engine. It does not host browsers
and it does not search for a personal Chrome installation.

## Packages

| Package | Responsibility |
| --- | --- |
| `browser` | Sessions, CDP transport, snapshots, revision-bound refs, actionability, managed runtimes, recordings, telemetry |
| `contract` | Closed operation, permission, and result registry |
| `script` | YAML, JSON, and Starlark plans against an open session |
| `mcp` | Policy-gated MCP stdio server |
| `productshot` | Shot and record packs |
| `processtree` | Vendored MIT subprocess-tree cancellation used by release and benchmark tools |
| `cmd/snapref` | Standalone CLI and live self-test |
| `cmd/snapref-release` | Reproducible local archive builder |
| `cmd/snapref-benchmark` | Diagnostic runtime comparison |

## Authority boundaries

Named CLI sessions are isolated profiles under `SNAPREF_HOME` (default
`~/.snapref/browser`). Headless managed runtimes are the default. `auto` uses
only verified managed runtimes or an explicit `SNAPREF_CHROME` override.

Snapshot refs are revision-bound. A later snapshot invalidates prior refs.
Actuation rechecks the live fingerprint and fails closed on drift.

MCP drive, host-read, host-write, and origin grants are independent. A grant
for one authority does not imply another.

## Host adapters

Host adapters may wrap this module. They are not part of the SnapRef release
archive. Private product handoffs stay in the host.

## Process lifecycle

`processtree` is vendored at reviewed `v0.1.0-rc.1`. Release and benchmark
commands use it so a cancelled `go build` or comparison child cannot leak
descendants. SnapRef does not depend on a sibling Go module.
