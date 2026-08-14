# Third-party notices

PageSkein's direct Go dependencies are tracked in `go.mod` and locked in
`go.sum`. Key dependencies include:

- chromedp and cdproto — MIT license;
- gobwas/ws — MIT license;
- go-json-experiment/json — BSD-3-Clause license;
- golang.org/x/sys — BSD-3-Clause license;
- go.starlark.net — BSD-3-Clause license;
- gopkg.in/yaml.v3 — Apache-2.0 and MIT licensed components.

`go list -m -json all` is the release-time authority for the exact dependency
graph. This notice is descriptive and does not replace upstream license text.

PageSkein can explicitly provision Google Chrome for Testing and Chrome
Headless Shell from Google's official availability metadata. Those browser
artifacts are not bundled in PageSkein source or release archives; operators
must review and comply with the upstream artifact terms when installing them.
