# Browser workflow benchmark

Run from the SnapRef module:

```sh
go run ./cmd/snapref-benchmark --out benchmark/latest.json
```

The harness uses one local fixture containing a password value and a mutable
button. It measures SnapRef, the current `agent-browser` npx package, and the
current `@playwright/mcp` npx package on:

- snapshot payload bytes and an explicit `ceil(bytes/4)` token approximation;
- unchanged-page snapshot replay equality;
- exact secret-marker leakage;
- behavior when an old ref is used after the button's accessible name changes;
- whether the invoked surface emits a durable per-command audit receipt.

The report records exact provider versions and timestamp. `audit_receipt=false`
means the directly invoked surface did not return/write one during this run; it
does not claim that an external wrapper cannot add logging. Likewise, the ref
test measures fail-closed behavior, not general locator quality.

This live comparison is intentionally outside `make verify`: it downloads
current competitor packages and is therefore non-deterministic. The SnapRef
release gate uses its own pinned tests; the comparison is a dated product
receipt.

## Runtime cost (machine-local receipt)

```sh
go run ./cmd/snapref-runtime-cost --runs 5 --out benchmark/runtime-cost.json
```

For each provider (SnapRef headless-shell, SnapRef chrome-for-testing and
agent-browser), each run opens a fresh session on a local fixture, takes a
snapshot and three screenshots, idles 10 s, and closes. The receipt records:
startup, screenshot and close time; process count, resident memory and CPU
while active and while idle; the time until every session process exits
(`drain_ms`) and what is still alive 30 s after close; whether any session
process registered as a Foreground app or the front app changed; whether the
screenshot reproduced the fixture's four colours exactly; disk use; and the
newest available version. The host's 1-minute load is stored with every run,
and providers alternate run by run, because other work moves the numbers.

`--providers` limits the run (for example `--providers snapref/headless-shell`).
