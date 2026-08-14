# Security policy

## Supported versions

PageSkein is a pre-release local package. Only the current `0.2.x` release
candidate receives security fixes. No hosted service or remote browser fleet is
part of this repository.

## Reporting

Do not open a public issue for a suspected vulnerability. Send a private report
to the repository owner with:

- affected version and operating system;
- the smallest safe reproduction;
- whether secrets, host files, browser profiles, or drive authority crossed a
  boundary;
- any known mitigation.

Expect an acknowledgement within seven days. Publication and disclosure timing
will be coordinated after a fix and regression test exist.

## Security boundaries

- Automatic launches use verified managed runtimes and isolated profiles; they
  do not search for or launch the user's daily browser installation. An
  external browser path is an explicit operator override.
- Runtime installation is explicit, HTTPS-only, official-host allowlisted,
  size/deadline bounded, traversal-safe, version-probed, locally digested, and
  atomically promoted. Local SHA-256 records are not publisher signatures.
- Session registry records are validated against their filename and canonical
  profile path. Close, expiry, and GC derive deletion paths from the validated
  session name and serialize against concurrent lifecycle operations.
- Standalone CLI sessions default to a detached idle watchdog. Keep-alive is an
  explicit, visible policy rather than an accidental zero-duration lease.
- Snapshot form values that resemble credentials are redacted in the page and
  scrubbed again in Go.
- MCP navigation, drive, caller-selected host reads, and caller-selected host
  writes require separate explicit grants.
- Snapshot refs are revision-bound and fingerprint-checked. Stale or changed
  targets fail closed; PageSkein does not semantically relocate them.
- Open shadow roots and same-origin frames are supported. Closed shadow roots
  and cross-origin frame contents are deliberately outside the page-script
  inventory boundary.
- The release command rejects the source tree, source-tree ancestors,
  filesystem roots, and symbolic-link output directories.
- The release command stages all artifacts before publication. A build failure
  does not change the selected output directory.
- Release artifact builds disable dependency downloads, workspace overrides,
  user Go settings, and automatic toolchain downloads.
- Existing output directories retain unrelated entries. PageSkein replaces
  only named regular artifacts and restores prior artifacts after a failed
  publication step.
- If restoration fails, PageSkein preserves the recovery staging directory and
  prints its path. Keep that directory until you recover the prior artifacts.
- Run release builds in an operator-controlled directory. The file transaction
  is not a boundary against an untrusted concurrent writer.
- The physical Windows+Chrome gate must pass before Windows support is publicly
  advertised; Wine evidence alone is not a support matrix.
