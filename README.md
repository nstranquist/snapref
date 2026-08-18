# SnapRef

SnapRef is a dependency-light, local-first browser automation core for agents:
isolated sessions, interactive snapshots with revision-bound executable refs,
typed actuation/assertion, scripts, policy-gated MCP, telemetry receipts, and
cross-platform lifecycle gates.

```sh
go install ./cmd/snapref
snapref self-test
```

Status: `0.2.0-rc.1`, local release candidate. The source is dogfooded by the
`ndev browser` adapter, but no public repository/tag has been published and
Windows support is not advertised until the physical Windows+Chrome gate passes.

**Routing:** for daily agent automation inside nicos-tools use `ndev browser`
(the adapter). Use this package when changing the engine, running
`snapref self-test`, or cutting a SemVer release of the core. The nicos-tools
factory has a repository-owned `.agents/skills/ndev-browser/SKILL.md` routing
guide; that private guide is not part of this standalone module.

## Packages

- `browser`: sessions, CDP, snapshots/refs, actionability, evidence, skills;
- `contract`: typed operation/permission/result registry;
- `script`: YAML/JSON/Starlark repeatable workflow engine;
- `mcp`: policy-gated MCP stdio server;
- `cmd/snapref`: standalone CLI and live self-test.

The standalone `skill` and `job-posting` commands emit generic typed extraction.
JobKit, workspace/demo, companion, and private product handoffs remain in the
`ndev browser` adapter.

## Golden path

```sh
snapref runtime install --runtime chrome-for-testing --channel stable
snapref runtime install --runtime headless-shell --channel stable
snapref runtime verify --all
snapref open demo
snapref exec demo navigate http://localhost:3000
snapref exec demo snapshot --wait-for-selector '[data-test="app-ready"]' --settle 1s
snapref exec demo click 'sr-…:e1'
snapref close demo --purge-profile
```

Headless is the default. `auto` uses only verified managed runtimes (or an
explicit `SNAPREF_CHROME` external override); it never searches or launches
the operator's personal browser installation. Headful and unpacked-extension
work routes to Chrome for Testing. Ordinary headless work uses Headless Shell
only after an exact-version local acceptance benchmark passes; otherwise it
uses managed Chrome for Testing. Runtime installation is explicit and is the
only browser command that downloads.

Named CLI sessions have a crash-safe 10-minute idle
lease; every browser operation renews it. Use `--idle-timeout` (or
`SNAPREF_BROWSER_IDLE_TIMEOUT`) for a longer bounded workflow and
`--keep-alive` only when persistence is intentional. Idle expiry retains the
named profile for cookie/state reuse; `snapref gc` previews stale dead
sessions and profiles, and `snapref gc --apply` reclaims them. Snapshot before
acting. Use the newest ref. MCP drive, host-read, host-write, and origin grants
are separate `SNAPREF_MCP_*` authorities.

Screenshots are full-page by default. YAML/JSON playbooks can declare
`screenshot_mode: full-page` for archival evidence or
`screenshot_mode: viewport` for exact fold/focus evidence; an individual step
can override the plan. Full-page capture measures the rendered document but
does not trigger application-specific lazy loading or scroll reveals. Each
script receipt records the effective mode and measured PNG width/height.

## Verify

```sh
make verify
make verify-release
```

`make verify` runs tests, the race detector, vet, native compilation, Windows
cross-compilation, and release-contract checks.

`make verify-release` also builds the release artifacts twice. It requires that
every artifact is byte-identical across both builds.

The artifact builder uses only the local module cache. It disables dependency
downloads, workspace overrides, user Go settings, and automatic toolchain
downloads.

The live `snapref self-test` verifies hydrated snapshot readiness. It also
verifies ref actuation across open shadow roots and same-origin frames.

`make release-dist` reproducibly builds CGO-free macOS arm64, Linux amd64, and
Windows amd64 archives with legal/security files and a `SHA256SUMS` manifest.
It does not publish them.

The release command builds all files in a sibling staging directory. It changes
only the named archives and `SHA256SUMS` in an existing output directory.

Unrelated output files remain unchanged. The command rejects filesystem roots,
the source tree, source-tree ancestors, symbolic-link outputs, and non-regular
managed artifacts.

If restoration fails after a publication error, the command prints and keeps
the recovery staging directory. Do not delete that directory until you restore
or copy the retained artifacts.

Use an operator-controlled output directory. The release transaction does not
protect against an untrusted process that changes the directory concurrently.

`EXTRACTION.json` pins a digest of every shared source path, canonical regular
file mode, and file byte. Windows checkout permissions are normalized to the
repository's required `0644` mode; other platforms fail on permission drift.
The extraction test fails when the factory and standalone source sets are not
synchronized through a reviewed digest update.

The versioned public artifact contracts live in `schemas/telemetry-v2.schema.json`
and `schemas/script-run-v2.schema.json`. Each release archive includes both
schemas and `docs/VERSIONING.md`.

Apache-2.0. See `SECURITY.md`, `docs/VERSIONING.md`, `NOTICE`,
`THIRD_PARTY_NOTICES.md`, and the bundled `third_party/licenses/` texts
before redistribution.
