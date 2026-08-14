# Third-party notices

PageSkein release archives include the license texts for the Go standard
library and every external module compiled into the CLI or its release builder.
The copied texts are under `third_party/licenses/`.

The reviewed dependency set is:

- `github.com/nstranquist/processtree` at the local `v0.0.0` release
  candidate — MIT;
- `github.com/chromedp/cdproto`
  `v0.0.0-20260321001828-e3e3800016bc` — MIT;
- `github.com/chromedp/chromedp` `v0.15.1` — MIT;
- `github.com/chromedp/sysutil` `v1.1.0` — MIT;
- `github.com/go-json-experiment/json`
  `v0.0.0-20260214004413-d219187c3433` — BSD-3-Clause;
- `github.com/gobwas/httphead` `v0.1.0` — MIT;
- `github.com/gobwas/pool` `v0.2.1` — MIT;
- `github.com/gobwas/ws` `v1.4.0` — MIT;
- `go.starlark.net` `v0.0.0-20260613233743-8ba36ccb83fb` —
  BSD-3-Clause;
- `go.yaml.in/yaml/v3` `v3.0.4` — MIT and Apache-2.0;
- `golang.org/x/sys` `v0.44.0` — BSD-3-Clause, with its patent grant
  included;
- the Go standard library — BSD-3-Clause, with its patent grant included.

The required Canonical notice from `go.yaml.in/yaml/v3` is reproduced in the
root `NOTICE` file and next to that module's license copy. The release
contract pins that exact module identity.

`go list -m -json all` and `go version -m <binary>` are the release-time
authorities for the exact module graph and build toolchain. The release
checklist requires a fresh graph review before publication.

PageSkein can explicitly provision Google Chrome for Testing and Chrome
Headless Shell from Google's official availability metadata. Those browser
artifacts are not bundled in PageSkein source or release archives. Operators
must review and comply with the upstream artifact terms before installation.
