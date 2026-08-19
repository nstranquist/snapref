# SnapRef browser engine

The `browser` package owns isolated Chromium sessions, CDP transport, typed
actionability, interactive snapshots, revision-bound executable refs, console
capture, screenshots, product-shot packs, screencasts, lifecycle locks,
telemetry, findings, and local skills.

The public CLI is `snapref`:

```sh
snapref doctor
snapref open demo
snapref exec demo navigate http://localhost:3000
snapref exec demo snapshot --wait-for-selector '[data-test="app-ready"]' --settle 1s
snapref exec demo click 'sr-…:e1'
snapref close demo --purge-profile
```

Defaults:

- headless, managed Chromium-family runtimes;
- isolated profiles under `~/.snapref/browser`;
- filesystem-loaded skills under `~/.snapref/skills`;
- `auto` never searches personal browser installations;
- `snapref runtime install` is the only runtime-download surface;
- Chrome for Testing handles headful/extension work; accepted Headless Shell
  handles ordinary headless work;
- `SNAPREF_CHROME` explicitly selects an external browser binary;
- `SNAPREF_DEBUG=1` exposes Chrome stderr.

```sh
snapref runtime install --runtime chrome-for-testing --channel stable
snapref runtime install --runtime headless-shell --channel stable
snapref runtime verify --all
snapref runtime benchmark --samples 30
snapref doctor
```

Standalone SnapRef benchmarking is diagnostic and non-mutating. Acceptance
recording is not part of this module.

Installed artifacts, immutable manifests, selections, and acceptance evidence
live under `<browser-home>/runtimes`. Manifests contain locally observed archive
and executable SHA-256 values; those are integrity records, not publisher
signatures. Normal launch, skill, script, MCP, and self-test operations never
download or update a runtime.

`NDEV_BROWSER_*` remains a compatibility alias for existing host adapters.
`NDEV_JSONL_MAX_BYTES` is the matching alias for `SNAPREF_JSONL_MAX_BYTES`.
New standalone usage should use `SNAPREF_*`.

Snapshots retry an initially empty interactive inventory for 750ms. A known SPA
should additionally pass a readiness selector; `Settle` requires two equal,
non-empty inventories. Open shadow roots and same-origin frames produce a typed
context chain. Closed roots and cross-origin frame contents stay outside this
page-script boundary.

Snapshot refs are ephemeral and fail closed. A new snapshot supersedes prior
refs, and every action rechecks the target's live fingerprint. SnapRef never
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
