module github.com/nstranquist/snapref

go 1.26

require github.com/nstranquist/processtree v0.0.0

replace github.com/nstranquist/processtree => ../processtree

require (
	github.com/chromedp/cdproto v0.0.0-20260321001828-e3e3800016bc
	github.com/chromedp/chromedp v0.15.1
	github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433
	github.com/gobwas/ws v1.4.0
	go.starlark.net v0.0.0-20260613233743-8ba36ccb83fb
	go.yaml.in/yaml/v3 v3.0.4
	golang.org/x/image v0.35.0
	golang.org/x/sys v0.47.0
)

require (
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	golang.org/x/text v0.33.0 // indirect
)
