# Changelog

All notable changes follow Keep a Changelog. Versioning follows Semantic
Versioning plus the schema rules in `docs/VERSIONING.md`.

## Unreleased

### Changed

- Shot-pack schema is `snapref.shot-pack.v1` and default directories are
  `snapref-shots/`. Abandoned in-process recordings (PID 0, no live goroutine)
  are reclaimed instead of blocking the next start. Telemetry rotate reads
  `SNAPREF_JSONL_MAX_BYTES` first.
- Public product identity is now SnapRef (`snapref` CLI, `SNAPREF_*` environment
  prefix, module `github.com/nstranquist/snapref`, catalog `product.snapref`).
  The factory adapter remains `ndev browser`.
- Runtime acceptance policy v4 requires an exact-version-matched managed pair,
  a SnapRef build identity bound to the running executable's SHA-256, rehashed
  typed receipts for self-test,
  tools, screenshots, observer, scripts, skills, and MCP, alternating launch
  order, deterministic fixture navigation, converged five-reading process-tree
  RSS windows, and macOS process-tree focus/window polling throughout each
  measured interval at a 100 ms target cadence. The Headless Shell focus result
  is the automatic-default gate; Chrome for Testing focus behavior is retained
  separately as comparator diagnostics so an activating fallback cannot defeat
  a correctly isolated Headless Shell target.
- Exact-version runtime install is a verified offline no-op when the immutable
  installation already exists. Runtime-kind locks now serialize launch,
  install, repair, and prune, and live owned sessions prevent replacement of
  the runtime version they reference.
- Extraction digests normalize Windows checkout permissions to the repository's
  canonical regular-file mode while rejecting permission drift elsewhere.

### Fixed

- Close, conflict replacement, and idle expiry retain native ownership
  authority when mutable runtime files drift, preventing an owned browser from
  becoming unkillable without weakening runtime-mutation checks.
- Page attachment tolerates the bounded gap between CDP target creation and
  WebSocket publication. Extension launches now fail closed unless the exact
  expected service-worker ID becomes ready.
- The legacy `LocateChrome` compatibility function accepts only an explicit
  external override; it no longer searches `PATH` or personal application
  directories.
- Standalone missing-runtime diagnostics now prescribe the available
  `snapref runtime install` command, and standalone routing text no longer
  implies that the factory-only browser skill is included.

### Security

- Full-history secret scans keep redaction fixtures in scope and allow only the
  exact synthetic values in the snapshot redaction test.
- Browser and page CDP endpoints reject redirects, oversized responses,
  non-literal loopback hosts, wrong ports or endpoint paths, userinfo, query or
  fragment data, and encoded/path-traversal forms; readiness also fails early
  when the launched process exits.
- Release builds reject filesystem roots, the source tree, source-tree
  ancestors, symbolic-link outputs, and non-regular managed artifacts.
- Release publication now uses a sibling staging directory and an exact-file
  rollback transaction. Build failures leave existing output unchanged.
- If rollback cannot restore every prior artifact, the recovery staging
  directory is preserved and reported instead of deleted.
- Release archives now carry the exact `go.yaml.in/yaml/v3` Canonical notice
  and reviewed license texts for the Go standard library and every external
  module compiled into the CLI or its release builder. The obsolete legacy
  YAML identity is rejected by contract.

### Added

- Standalone race, Windows cross-compilation, and byte-reproducibility release
  gates.
- A shared-source extraction digest for the factory and standalone trees.
- Versioning policy and both public JSON schemas in each binary archive.

## 0.2.0-rc.1 - 2026-07-16

### Added

- Explicit official Chrome for Testing and Chrome Headless Shell provisioning,
  immutable local manifests, atomic selections, deep verification, dry-run
  pruning, capability-aware diagnostics, and closed JSON runtime envelopes.
- Deterministic functional/visual/focus/startup/RSS acceptance benchmarking;
  exact-version passing evidence may promote Headless Shell for ordinary
  automatic headless work.
- Runtime identity and capabilities in session, list, MCP/driver, observer, and
  bounded telemetry projections.

- The standalone CLI now applies the same detached idle-watchdog policy as the
  `ndev browser` adapter: a telemetry-calibrated 10-minute default lease,
  explicit `--keep-alive`, configurable `--idle-timeout`, visible lifecycle
  metadata, and a dry-run-first `gc` command.
- Garbage-collection reports now inventory stale live keep-alive sessions as
  audit-only findings without making them eligible for automatic removal.

### Changed

- `auto` is now managed-only: it never searches common personal-browser paths.
  `SNAPREF_CHROME` and `NDEV_BROWSER_CHROME` remain explicit external escape
  hatches. Headful and unpacked-extension requests route to Chrome for Testing.
- Every launch-bearing SnapRef/NDev path accepts or propagates a typed runtime
  request, including stateless skills, batches, scripts, local-demo, companion
  probes, and self-test.
- Managed runtime identity replaces personal-browser app identity as the
  isolation boundary. Chromium 151's `--no-startup-window` was evaluated and
  intentionally omitted because it intermittently suppresses the required
  initial page target in both allocator and persistent launch modes.

- Browser startup uses one end-to-end readiness budget and synchronously reaps
  failed child processes without deleting a pre-existing reusable profile.
- Garbage collection serializes against open/close/watchdog operations and
  rechecks the current generation before deleting a registry entry or profile.

### Security

- Session records fail closed on filename/name mismatches, corrupt JSON,
  unknown lifecycle policies, and noncanonical profile paths. Cleanup derives
  its deletion target from the validated session name instead of trusting
  persisted JSON.
- Unix ownership checks now bind a session to process creation time as well as
  executable/profile identity; stale watchdogs cannot broaden a persisted
  profile-purge decision.

## 0.1.0-rc.1 - 2026-07-12

### Added

- Isolated CDP browser sessions with ownership-checked lifecycle control.
- Interactive snapshots with secret redaction, executable revision-bound refs,
  adaptive empty-inventory readiness, explicit readiness selectors, and bounded
  settling.
- Typed actionability, assertions, console capture, screenshots, YAML/JSON/
  Starlark scripts, and policy-gated MCP tools.
- True persistent-session full-page capture plus explicit `viewport` and
  `full-page` playbook screenshot modes at plan or step scope. Both modes use
  deterministic CSS-pixel clips rather than host device-scale pixels.
- Script-run receipts record each screenshot's effective capture mode and PNG
  dimensions so visual evidence can be audited without reopening the artifact.
- Open-shadow-root and same-origin-frame discovery and actuation.
- Versioned telemetry, findings, adoption ratchets, and Windows lifecycle gates.
- Generic `skill` and `job-posting` CLI extraction with Nicos handoffs excluded.
- Existing-target and extension-service-worker evaluation over direct websocket
  CDP, preserving targets instead of letting chromedp close them on cleanup.
- Versioned telemetry and script-run JSON schemas.
- Deterministic macOS/Linux/Windows release archives and SHA-256 manifest.
- Dated live SnapRef/agent-browser/Playwright MCP comparison harness.

### Security

- Host read, host write, navigation, arbitrary JavaScript, and page actuation
  use separate grants.
- Changed or superseded refs fail closed.
