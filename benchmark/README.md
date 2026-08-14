# Browser workflow benchmark

Run from the PageSkein module:

```sh
go run ./cmd/pageskein-benchmark --out benchmark/latest.json
```

The harness uses one local fixture containing a password value and a mutable
button. It measures PageSkein, the current `agent-browser` npx package, and the
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
current competitor packages and is therefore non-deterministic. The PageSkein
release gate uses its own pinned tests; the comparison is a dated product
receipt.
