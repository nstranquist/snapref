# PageSkein — agent notes

Local-first browser automation core (open-core candidate). Dogfooded by
`ndev browser`; this tree is the extractable product.

## Commands

```sh
make verify              # test + vet + build + release-check
go test ./...
go run ./cmd/pageskein version
go run ./cmd/pageskein self-test
```

## Routing

- Daily agent automation **inside nicos-tools**: `ndev browser` (adapter).
- Engine changes, self-test, SemVer releases: this package.
- Skill: `.agents/skills/ndev-browser/SKILL.md` § ndev browser vs PageSkein.

## Publication boundary

- Apache-2.0 core; nicos adapters and any Team control plane stay closed.
- No public repo until Windows+Chrome physical receipt + naming clearance.
- Job-search shortlist: **P2** after docs-puller / Openbook / Hidden Bar / JobKit.

## Token efficiency

Prefer `README.md` + `SECURITY.md` + `contract/` over whole-tree greps.
Use `pageskein self-test` for end-to-end engine confidence; reserve managed
Chrome downloads for explicit runtime install work.
