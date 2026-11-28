# Release checklist

Public source is at https://github.com/nstranquist/snapref. The current
tag is `v0.2.5` (2026-11-28). Complete the remaining items before a signed 1.0.

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
  and `CHANGELOG.md` agree (`0.2.5` / tag `v0.2.5`).
- [x] Verify every entry in `SHA256SUMS` against the final archives (done for
  `v0.2.0-rc.2`; `v0.2.0`, `v0.2.1`, `v0.2.2`, `v0.2.3`, `v0.2.4` and `v0.2.5` have no published archives yet).
- [x] Keep Nicos adapters and control-plane code outside this repository.
- [x] Obtain explicit approval before you create a remote, push, tag, or
  publish. Public repo, `v0.2.1`, `v0.2.2`, `v0.2.3` and `v0.2.4` tags (unsigned, no GitHub release page) and
  the `v0.2.0-rc.2` unsigned GitHub prerelease exist. GPG-signed artifacts do not.
