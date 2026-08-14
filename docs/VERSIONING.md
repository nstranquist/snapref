# Version and schema policy

PageSkein uses Semantic Versioning 2.0.0 for the Go packages, CLI, and npm
metadata. `VERSION` is canonical. `package.json` and the CLI's Go `version`
constant are verified mirrors.

Run `nship release verify pageskein` before a governed release. A difference
between these version values makes the command fail.

- Patch: compatible bug/security fixes and new optional output fields.
- Minor: compatible commands, tools, options, and schema additions.
- Major: removed/renamed public APIs, changed defaults that can alter authority,
  or incompatible schema changes.

While PageSkein remains below 1.0, a reviewed breaking change requires a minor
bump. Release candidates use the allowlisted `rc` or `beta` channels. The
governed release flow is `intent` → `plan` → `apply --input-sha256 <digest>
--confirm` → review/commit → `tag --confirm`. The reviewed plan supplies the
digest. Tags use `pageskein/v{version}`.

The local release candidate uses the exact sibling ProcessTree replacement in
`go.mod`. Before publication, require a reviewed ProcessTree SemVer version.
Update the release-contract test in the same reviewed change.

Machine-readable events and manifests carry their own integer
`schema_version`. Consumers must ignore unknown optional fields. Removing a
field, changing its meaning/type, or changing an enum incompatibly requires a
new schema version. Old readers remain supported for at least one minor release.

Executable snapshot refs are ephemeral capabilities, not durable IDs. Their
text format can gain a new prefix only in a minor release. Previously issued
refs are valid only for the latest inventory and live fingerprint by design.
