# Workflow: revision-bound refs

This is the interview clip. A `sr-…` ref is executable only for the
inventory it was captured against. After the next snapshot, SnapRef fails
closed. It does not silently relocate.

```sh
go run ./cmd/snapref runtime install --runtime chrome-for-testing --channel stable
go run ./cmd/snapref demo ref-integrity
```

Expected receipt: `ok: true`, `clicked: true`, `stale_rejected: true`,
and `first_revision` different from `second_revision`.

`self-test` stays the hydration / shadow / iframe inventory check. This
demo is the fail-closed ref proof.
