# Contributing

Use `SNAPREF_*` environment names. Do not add factory-only names to this
module.

```sh
make init
make verify
```

`make verify` runs tests with `-short`, the race detector, vet, native
compilation, Windows cross-compilation, and release-contract checks. It
does not launch Chrome. `make test-live` runs the same tests without
`-short` after `make init`. `make verify-release` also builds the archives
twice and requires byte-identical output.

Rules:

- Keep host adapters and private product handoffs out of this tree.
- Do not search for or launch a personal browser installation.
- Runtime downloads happen only through `snapref runtime install` or `make init`.
- Prefer `README.md`, `SECURITY.md`, `docs/ARCHITECTURE.md`, and `contract/`
  over whole-tree searches.
- `snapref self-test` is the live engine check. It needs a managed runtime.
- `snapref demo mcp-policy` is the MCP grant receipt and does not launch Chrome.
- `snapref demo ref-integrity` is the stale-ref receipt. It needs a managed runtime.
- Public fixtures must be synthetic. Do not commit operator profiles or
  session recordings.

Report security issues through [SECURITY.md](SECURITY.md), not a public issue.
