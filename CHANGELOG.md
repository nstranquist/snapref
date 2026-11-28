# Changelog

All notable changes follow Keep a Changelog. Versioning follows Semantic
Versioning plus the schema rules in `docs/VERSIONING.md`.

## Unreleased

## 0.2.5 - 2026-11-28

### Added

- `DeviceEmulation.ColorScheme` (`light` or `dark`) emulates
  `prefers-color-scheme` with the session size. It persists, the viewport
  holder keeps it between calls and repairs a foreign media override, and a
  one-shot capture size keeps the session's scheme unless it names its own.
- `LaunchOptions.ColorScheme` sets the scheme for stateless screenshot, eval
  and fetch before the page loads.

### Changed

- Every eval path awaits a returned Promise and returns the value by value:
  `Eval`, `RemoteEval`, `EvaluateOn` (script and MCP eval) and `EvalInTarget`.
  `EvalAwait`, `EvalPromise` and `EvalPromiseInTarget` stay as aliases.

### Fixed

- A full-page `screenshot-current` of a scrolled page starts at the top, so
  sticky and fixed elements draw in place, then restores the scroll position.

## 0.2.4 - 2026-11-24

### Changed

- Added Chromium launch hygiene flags (`--disable-background-networking`,
  `--disable-component-update`, `--disable-sync`, `--disable-default-apps`,
  `--disable-domain-reliability`, `--disable-client-side-phishing-detection`,
  `--disable-breakpad`, `--no-pings`, `--metrics-recording-only`,
  `--password-store=basic`, `--use-mock-keychain`, and `--disable-extensions`
  when no extension is loaded) to cut background CPU, RAM, disk, and macOS
  Keychain overhead on managed headless sessions.

## 0.2.3 - 2026-11-18

### Fixed

- Screenshots of a session with a held viewport come out in device pixels
  and no longer reset the page's device scale factor. Chrome builds a
  clipped or full-page capture from the device metrics of the DevTools
  session that sends it, then restores that session's own metrics on the
  page. An exec connection has no override while the holder serves, so a
  `--mobile` (DPR 2) capture came out at 1x and left the page at DPR 1.
  While a holder serves the session, `RemoteScreenshotCurrentWithOptions`
  and `RemoteScreenshotWithCaptureAndWait` now send the capture to the
  holder, which runs it on the connection that owns the override (a file
  request per capture; it fails closed when no holder takes it within 3 s).
- The holder re-applies the viewport when the page's device scale factor
  drifts, not only its width, and nudges the scale factor first when it
  re-applies the same spec: Chrome ignores an exact repeat of the override
  a connection set last. A one-shot screenshot size now restores DPR 2 as
  well as the width.

## 0.2.2 - 2026-11-17

### Fixed

- A viewport set on a persistent session now holds between calls. Chrome
  keeps an Emulation override only while the DevTools session that set it is
  open, and closing that session clears it for the whole page, so between two
  calls the page went back to the window width and `(max-width)` listeners
  fired on every call. A holder process (`browser.HoldSessionEmulation`,
  started through `browser.EmulationHolderLauncher`) keeps one connection
  with the override applied; connections apply nothing while it serves the
  spec, a one-shot screenshot size asks it to re-apply, and clearing the
  viewport stops it. `browser.DetachedHolderLauncher` and
  `browser.RunHoldViewport` give a CLI the hidden `__hold-viewport`
  subcommand. Without a launcher, connections re-apply per call as before.

## 0.2.1 - 2026-11-17

### Added

- `cmd/snapref-runtime-cost` records a machine-local runtime cost receipt
  (startup, screenshot and close time, process count, memory, CPU and disk)
  for SnapRef headless-shell, SnapRef Chrome for Testing and agent-browser.
  `benchmark/runtime-cost.json` is the first receipt.
- `browser.RuntimeMissingError` reports a managed runtime that is not
  installed. Operation telemetry classifies it as `runtime_missing`, and a
  command against a session that is not open as `session_not_open`, instead of
  a generic usage error.
- `browser.AdmissionRuntimeKind` predicts the runtime an auto request will
  launch, without verifying the binary, so an admission layer can price the
  session (headless-shell or Chrome for Testing) before it starts.

### Fixed

- The idle watchdog now waits on the browser process exit (kqueue
  `NOTE_EXIT` on macOS and BSD, a one-second poll elsewhere), so it ends
  within about a second of `close` instead of after its next minute check.

## 0.2.0 - 2026-11-16

### Fixed

- Telemetry identifies reproducible builds by embedded source revision or digest
  when Go VCS metadata is absent, so reviewed releases do not report `dev`.
- Script failure events and stored run manifests retain typed, bounded reason
  codes without raw page exception text. Failed run summaries keep the first
  failed step's cause and nonzero exit. Immediate output keeps full diagnostics.

### Added

- Persistent sessions keep a device emulation override (`DeviceEmulation`:
  width, height, device scale factor, mobile, touch). Chrome drops
  `Emulation.*` overrides when the DevTools connection that set them closes,
  so the session record stores the override and every new connection
  re-applies it. `SetSessionEmulation` and `RemoteSetDeviceEmulation` set or
  clear it; `RemoteSetViewport` records a desktop override the same way.
- `RemoteScreenshotCurrentWithOptions` and
  `RemoteSaveScreenshotCurrentWithOptions` capture the current page with a
  one-shot emulation, an optional same-connection navigation and an optional
  visible-selector wait. The page then returns to the persisted emulation.
- Headless launches pass `--hide-scrollbars`, so captures use the full
  emulated width.
- Console capture kind `csp` returns typed Content-Security-Policy issues from
  the CDP Audits domain: directive, violation type, blocked URL, report-only
  flag, and source location. Report-only violations count, so a Report-Only
  soak can be gated without the browser's batched report upload.
- Console capture accepts Navigate or Reload together with Eval: it loads the
  page, waits for the new document's load event, then runs the eval in the
  same capture. `ConsoleCaptureOptions.Validate` is the one rule set for the
  CLI, MCP, and script surfaces.
- `RemoteConsoleCaptureResult` returns `ConsoleCapture{Entries, Eval,
  NavigatedTo}`. Drivers opt in through `ConsoleCapturer`; `CaptureConsole`
  falls back to `Driver.Console`. The MCP `browser_console` payload carries
  `eval` (`settled` or `navigated`) and `navigated_to`.
- A request for a missing session returns `SessionMissingError`
  (class usage). When the command log shows that the idle watchdog closed the
  session, the error gives the close time, the lease, and the reopen command.
  `SessionOpenCommand` sets the command a host CLI quotes.

### Fixed

- Console capture is never falsely clean. A navigation Chrome could not
  complete, an eval that threw or rejected, a load that never fired before a
  queued eval, and an eval that did not settle in 30 seconds each return a
  typed error (`ConsoleNavigateError`, `ConsoleEvalError`,
  `ConsoleLoadTimeoutError`, `ConsoleEvalTimeoutError`) with the entries
  collected. Before, the capture skipped command responses and read as clean.
- The console listen window starts when the eval settles, not when it is
  sent. An eval that awaited longer than the window lost its later output,
  and a late rejection read as clean.
- An eval whose page navigates away (a sign-in redirect) is state
  `navigated`, not a thrown eval. Capture continues on the new document.
- Browser stats keep assertion failures (such as a tripped console gate) and
  synthetic test rows out of the tool error rate, and report
  `assertion_failures` separately.
- The remote screenshot timeout bounds the whole CDP sequence (bring to
  front, frame, metrics, and capture), not only `Page.captureScreenshot`.
  On timeout it closes the page transport, so a websocket read that stays
  blocked after its context expires cannot hang the screenshot.

## 0.2.0-rc.2 - 2026-11-02

### Added

- Native select actuation is available through the Go driver, CLI, MCP,
  YAML/JSON scripts, and Starlark. It matches one enabled option by normalized
  visible label and emits native input and change events.
- Revision-bound actions use a one-second transition retry only while the page
  URL, snapshot mapping, selector, root context, and semantic fingerprint stay
  unchanged. Navigation fails closed without copying URL query data into the
  stale-ref error.
- `snapref demo mcp-policy` prints a drive/origin grant receipt through the
  shipped MCP stdio server. Snapshot runs without drive; click and navigate
  do not. An origin allowlist refuses off-list hosts.
- `snapref demo ref-integrity` snapshots a local fixture, clicks the live
  `sr-…` ref, snapshots again, and proves the old ref fails closed.
- GitHub Actions Ubuntu `self-test` job installs Chrome for Testing and
  runs `snapref self-test` plus `demo ref-integrity`.

### Changed

- README tagged-install block is `v0.2.0-rc.1` `self-test` only. Wedge
  `demo` receipts are checkout commands (`go run ./cmd/snapref demo …`).
  Do not pair `go install @v0.2.0-rc.1` with `snapref demo`.
- README tagged install now targets `v0.2.0-rc.2`, which includes the
  `demo mcp-policy` and `demo ref-integrity` receipts.
- `make verify` and GitHub Actions `go` pass `-short`, so they do not
  launch Chrome after `make init`. Live Chrome is `make test-live`,
  `snapref self-test`, and `snapref demo ref-integrity`.
- Failed `snapref demo mcp-policy` emits the partial JSON receipt, then
  returns the error, matching `demo ref-integrity`.
- ProcessTree `v0.1.0-rc.1` is vendored at `processtree/`. `go.mod` no longer
  uses a sibling `replace`.
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

- The Ubuntu live `self-test` CI job allows unprivileged user namespaces, so
  Chrome for Testing starts with its sandbox enabled and publishes
  `DevToolsActivePort`.
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

- `golang.org/x/image` is upgraded to 0.39.0 to fix the reachable malicious
  SFNT memory-allocation vulnerability GO-2026-4962. The required indirect
  `golang.org/x/text` dependency is upgraded to 0.36.0.
- Select telemetry retains only the visible-label byte count. Snapshot names
  for form controls use labels and attributes, not textarea or option content.
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
