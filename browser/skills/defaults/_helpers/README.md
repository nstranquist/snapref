# Shared JS helpers

This directory parallels browser-harness's
`agent-workspace/agent_helpers.py` — it's the shared scratchpad for JS
helpers that several domain skills need.

**Auto-prepended.** `RunExtractor` reads every `*.js` under
`<root>/_helpers/` (alphabetical order) and prepends them into the
extractor's scope before evaluation. Each file should declare top-level
`function` (or `const` / `let`) bindings; they become visible to every
domain's `extract-*.js`. No imports needed — just call the helper by
name from any extractor.

## What lives here

- `firstText.js` — `firstText(selectors[])` returns the trimmed
  textContent of the first selector that matches with non-empty text.
  Used by LinkedIn extractors and any domain that wants selector-
  fallback semantics.

## Adding a helper

1. Drop a `.js` file here. Convention: one helper per file, name the
   file after the function (e.g. `parseCount.js`).
2. Use top-level `function name() { … }` declarations. Avoid setting
   global side-effects (`window.foo = …`) — the loader wraps everything
   in an IIFE so globals don't leak across calls.
3. Re-run any extractor that uses the helper; no SnapRef or `ndev`
   rebuild is needed.

## Why this matters

Without auto-prepend, every domain extractor has to redefine its own
`firstText`/`parseCount`/etc. inline, and a fix to one helper has to
be hand-replicated across N files. The auto-prepend gives us
browser-harness's `agent_helpers.py` injection model in JS.
