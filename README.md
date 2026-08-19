# SnapRef

SnapRef is a dependency-light, local-first browser automation core for agents:
isolated sessions, interactive snapshots with revision-bound executable refs,
typed actuation/assertion, scripts, policy-gated MCP, telemetry receipts, and
cross-platform lifecycle gates.

```sh
go install ./cmd/snapref
make init
snapref self-test
```

Status: `0.2.0-rc.1`, local release candidate. No public repository or tag has
been published. Windows support is not advertised until a physical
Windows+Chrome gate passes.

## Packages

- `browser`: sessions, CDP, snapshots/refs, actionability, evidence, skills;
- `contract`: typed operation/permission/result registry;
- `script`: YAML/JSON/Starlark repeatable workflow engine;
- `mcp`: policy-gated MCP stdio server;
- `cmd/snapref`: standalone CLI and live self-test.

The standalone `skill` and `job-posting` commands emit generic typed extraction
only. Host-specific product handoffs are outside this module.

## Golden path

```sh
make init
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
A digest mismatch means the checked-out tree drifted from the reviewed source
lock and must be resynchronized before a release.

The versioned public artifact contracts live in `schemas/telemetry-v2.schema.json`
and `schemas/script-run-v2.schema.json`. Each release archive includes both
schemas and `docs/VERSIONING.md`.

## Architecture

SnapRef is a local engine, not a hosted browser fleet.

- `browser` owns isolated Chromium sessions, CDP, snapshots, revision-bound
  refs, actionability, recordings, and managed runtimes.
- `contract` is the typed operation and permission registry.
- `script` runs YAML/JSON/Starlark plans against an open session.
- `mcp` is a policy-gated stdio server. Drive, host-read, host-write, and
  origin grants are separate authorities.
- `cmd/snapref` is the standalone CLI. Host adapters such as `ndev browser`
  live outside this module.
- `processtree` is a vendored MIT copy used only for bounded subprocess
  cancellation in release and benchmark tools.

See `docs/ARCHITECTURE.md`.

## Usage

```sh
snapref open demo
snapref exec demo navigate https://example.com
snapref exec demo snapshot --wait-for-selector body --settle 1s
snapref exec demo click 'sr-…:e1'
snapref close demo --purge-profile
snapref mcp
```

`snapshot` is the discovery default. Act on the newest revision-bound ref.
`eval` is an escape hatch, not the discovery path. MCP drive, host-read,
host-write, and origin grants are separate explicit authorities.

## Configuration

| Variable | Purpose |
| --- | --- |
| `SNAPREF_HOME` | Session, profile, recording, and telemetry root (default `~/.snapref/browser`) |
| `SNAPREF_SKILLS` | Skill directory (default `~/.snapref/skills`) |
| `SNAPREF_CHROME` | Explicit external Chromium path; never a personal-browser search |
| `SNAPREF_BROWSER_IDLE_TIMEOUT` | Named-session idle lease (default `10m`) |
| `SNAPREF_JSONL_MAX_BYTES` | Hot telemetry rotate threshold (default 64KiB; `0` disables) |
| `NDEV_JSONL_MAX_BYTES` | Compatibility alias for `SNAPREF_JSONL_MAX_BYTES` (host adapters) |
| `SNAPREF_MCP_ALLOW_DRIVE` | MCP actuation |
| `SNAPREF_MCP_ALLOW_HOST_READ` | MCP caller-selected playbook reads |
| `SNAPREF_MCP_ALLOW_HOST_WRITE` | MCP caller-selected screenshot/pack paths |
| `SNAPREF_MCP_ALLOW_ORIGINS` | MCP navigate/fetch allowlist |
| `SNAPREF_DEBUG` | Chrome stderr |

Runtime install is the only command that downloads. Recording packs that
encode video or GIF require `ffmpeg` on PATH; still-shot packs do not.

## Troubleshooting

- `no Chromium-class browser found`: run `snapref runtime install --runtime chrome-for-testing --channel stable`.
- Session missing after idle: named sessions expire after 10 minutes; the profile remains until `close --purge-profile` or `gc --apply`.
- Stale executable ref: take a new `snapshot` and use the newest `sr-…` ref.
- MCP actuation refused: set `SNAPREF_MCP_ALLOW_DRIVE=1` (or `--allow-drive`) and an origin allowlist if you restrict hosts.
- `already recording` after a crash: the next in-process start reclaims abandoned in-process state. A detached start that failed before a PID was stamped must be removed: delete `$SNAPREF_HOME/recordings/<name>` (default `~/.snapref/browser/recordings/<name>`). If a detached PID is still alive, stop that process first.
- Video encode fails: install `ffmpeg`; still packs do not need it. The CLI prints a bounded ffmpeg stderr on failure.

Apache-2.0. See `SECURITY.md`, `docs/VERSIONING.md`, `NOTICE`,
`THIRD_PARTY_NOTICES.md`, and the bundled `third_party/licenses/` texts
before redistribution.
