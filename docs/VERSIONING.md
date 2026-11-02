# Version and schema policy

SnapRef uses Semantic Versioning 2.0.0 for the Go packages, CLI, and npm
metadata. `VERSION` is canonical. `package.json` and the CLI's Go `version`
constant are verified mirrors.

Run `nship release verify snapref` before a governed release. A difference
between these version values makes the command fail.

- Patch: compatible bug/security fixes and new optional output fields.
- Minor: compatible commands, tools, options, and schema additions.
- Major: removed/renamed public APIs, changed defaults that can alter authority,
  or incompatible schema changes.

While SnapRef remains below 1.0, a reviewed breaking change requires a minor
bump. Release candidates use the allowlisted `rc` or `beta` channels. The
governed release flow is `intent` → `plan` → `apply --input-sha256 <digest>
--confirm` → review/commit → `tag --confirm`. The reviewed plan supplies the
digest. Tags use `snapref/v{version}`.

ProcessTree `v0.1.0-rc.1` is vendored at `processtree/` inside this module.
`go.mod` must not contain a `replace` directive. A later reviewed change may
switch back to a published ProcessTree SemVer once that module has a remote.

Machine-readable events and manifests carry their own integer
`schema_version`. Consumers must ignore unknown optional fields. Removing a
field, changing its meaning/type, or changing an enum incompatibly requires a
new schema version. Old readers remain supported for at least one minor release.

Executable snapshot refs are ephemeral capabilities, not durable IDs. Their
text format can gain a new prefix only in a minor release. Previously issued
refs are valid only for the latest inventory and live fingerprint by design.
A bounded transition retry does not change that contract: it requires an
unchanged page URL, snapshot mapping, selector, root context, and fingerprint.
