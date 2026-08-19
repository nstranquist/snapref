# SnapRef — contributor notes

Local-first browser automation core. Public commands live on `snapref`.

## Commands

```sh
make verify
go test ./...
go run ./cmd/snapref version
go run ./cmd/snapref self-test
```

## Rules

- Use `SNAPREF_*` environment variables. Do not add factory-only names to
  this module.
- Prefer `README.md`, `SECURITY.md`, and `contract/` over whole-tree greps.
- `snapref self-test` is the end-to-end engine check. Runtime downloads happen
  only through `snapref runtime install`.
- Keep host adapters and private product handoffs out of this tree.
