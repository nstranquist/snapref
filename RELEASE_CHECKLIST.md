# Release checklist

SnapRef is not released. Complete every item before you create a public
repository, tag, module version, package, or binary release.

- [x] Clear the public name and module path with the repository owner. Public name is SnapRef; module path is `github.com/nstranquist/snapref`.
- [x] Run `make verify-release` from a clean standalone checkout.
- [x] Run `gitleaks git . --redact=100 --no-banner` and resolve every finding.
- [x] Confirm that `EXTRACTION.json` matches the reviewed factory source.
- [x] Run `snapref self-test` with the selected managed runtime.
- [ ] Pass the physical Windows and Chrome gate. Wine evidence is not enough.
- [x] Remove the sibling ProcessTree `replace`. ProcessTree `v0.1.0-rc.1` is
  vendored at `processtree/` (reviewed MIT copy). A later change may switch
  to a published ProcessTree SemVer once that module has a remote.
- [x] Review the dependency graph with `go list -m -json all` and confirm
  every compiled module is indexed under `third_party/licenses/`.
- [x] Review `NOTICE`, `THIRD_PARTY_NOTICES.md`, every bundled license text,
  and the managed-browser terms.
- [x] Confirm that `VERSION`, `package.json`, the CLI version, the intended tag,
  and `CHANGELOG.md` agree (`0.2.0-rc.1`; no public tag yet).
- [x] Verify every entry in `SHA256SUMS` against the final archives.
- [x] Keep Nicos adapters and control-plane code outside this repository.
- [ ] Obtain explicit approval before you create a remote, push, tag, or
  publish.
