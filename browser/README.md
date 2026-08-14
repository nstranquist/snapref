# PageSkein browser engine

The `browser` package owns isolated Chromium sessions, CDP transport, typed
actionability, interactive snapshots, revision-bound executable refs, console
capture, screenshots, lifecycle locks, telemetry, findings, and local skills.

The public CLI is `pageskein`:

```sh
pageskein doctor
pageskein open demo
pageskein exec demo navigate http://localhost:3000
pageskein exec demo snapshot --wait-for-selector '[data-test="app-ready"]' --settle 1s
pageskein exec demo click 'sr-…:e1'
pageskein close demo --purge-profile
```

Defaults:

- headless, managed Chromium-family runtimes;
- isolated profiles under `~/.pageskein/browser`;
- filesystem-loaded skills under `~/.pageskein/skills`;
- `auto` never searches personal browser installations;
- `pageskein runtime install` is the only runtime-download surface;
- Chrome for Testing handles headful/extension work; accepted Headless Shell
  handles ordinary headless work;
- `PAGESKEIN_CHROME` explicitly selects an external browser binary;
- `PAGESKEIN_DEBUG=1` exposes Chrome stderr.

```sh
pageskein runtime install --runtime chrome-for-testing --channel stable
pageskein runtime install --runtime headless-shell --channel stable
pageskein runtime verify --all
pageskein runtime benchmark --samples 30
ndev browser runtime consumer-evidence --out ~/.nicos-dev/browser/runtimes/runtime-consumer-evidence.json
ndev browser runtime benchmark --samples 30 --consumer-evidence ~/.nicos-dev/browser/runtimes/runtime-consumer-evidence.json --record
pageskein doctor
```

Standalone PageSkein benchmarking is diagnostic and non-mutating. NDev owns
acceptance recording because it can produce the required consumer evidence in
the exact executable being authorized.

Installed artifacts, immutable manifests, selections, and acceptance evidence
live under `<browser-home>/runtimes`. Manifests contain locally observed archive
and executable SHA-256 values; those are integrity records, not publisher
signatures. Normal launch, skill, script, MCP, and self-test operations never
download or update a runtime.

The `NDEV_BROWSER_*` environment aliases remain only so the dogfooded Nicos
adapter can migrate without breaking existing sessions. New standalone usage
should use `PAGESKEIN_*`.

Snapshots retry an initially empty interactive inventory for 750ms. A known SPA
should additionally pass a readiness selector; `Settle` requires two equal,
non-empty inventories. Open shadow roots and same-origin frames produce a typed
context chain. Closed roots and cross-origin frame contents stay outside this
page-script boundary.

Snapshot refs are ephemeral and fail closed. A new snapshot supersedes prior
refs, and every action rechecks the target's live fingerprint. PageSkein never
silently relocates a changed ref.

Persistent-session screenshots have two distinct contracts. Viewport capture
reads the current CSS visual viewport from CDP layout metrics. Full-page capture
uses the content metrics and an explicit compositor clip, so the PNG includes
the entire scroll height from header through footer. Callers should keep
viewport captures for fold checks and use full-page captures for archival route evidence. The
script engine defaults to full-page and accepts `screenshot_mode: viewport` or
`screenshot_mode: full-page` at plan or screenshot-step scope. Both modes use
CSS-pixel clips, so a declared 1440×1000 viewport produces a 1440×1000 PNG even
on a 2× display.
