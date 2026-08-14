# Release checklist

PageSkein is not released. Complete every item before you create a public
repository, tag, module version, package, or binary release.

- [ ] Clear the public name and module path with the repository owner.
- [ ] Run `make verify-release` from a clean standalone checkout.
- [ ] Run `pageskein self-test` with the selected managed runtime.
- [ ] Pass the physical Windows and Chrome gate. Wine evidence is not enough.
- [ ] Replace the sibling ProcessTree directive with a reviewed SemVer
  dependency. Update the release-contract test in the same change.
- [ ] Review the dependency graph with `go list -m -json all`.
- [ ] Review `THIRD_PARTY_NOTICES.md` and the managed-browser terms.
- [ ] Confirm that `VERSION`, `package.json`, the CLI version, the intended tag,
  and `CHANGELOG.md` agree.
- [ ] Verify every entry in `SHA256SUMS` against the final archives.
- [ ] Keep Nicos adapters and control-plane code outside this repository.
- [ ] Obtain explicit approval before you create a remote, push, tag, or
  publish.
