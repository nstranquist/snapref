# Examples

The wedge proofs are `snapref demo` receipts, not public-site YAML.

```sh
go run ./cmd/snapref demo mcp-policy
go run ./cmd/snapref runtime install --runtime chrome-for-testing --channel stable
go run ./cmd/snapref demo ref-integrity
```

- [ref-integrity](ref-integrity/README.md): snapshot, click `sr-…`, snapshot again, fail closed
- [mcp-policy](mcp-policy/README.md): snapshot ungated, click/navigate denied, origin block

`open-and-snapshot.yaml` is a generic playbook sample. It is not the
interview clip.

```sh
snapref open demo
snapref script run examples/open-and-snapshot.yaml --session demo --allow-drive
snapref close demo --purge-profile
```
