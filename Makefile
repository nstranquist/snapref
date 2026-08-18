.PHONY: verify verify-release test race vet build cross release-check release-repro release-dist

verify: test race vet build cross release-check

verify-release: verify release-repro

test:
	GOWORK=off go test -mod=readonly ./...

race:
	GOWORK=off go test -mod=readonly -race ./...

vet:
	GOWORK=off go vet -mod=readonly ./...

build:
	GOWORK=off go test -mod=readonly -run '^$$' ./cmd/snapref

cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go test -mod=readonly -run '^$$' -exec=true ./...

release-check:
	GOWORK=off go test -mod=readonly -run '^TestRelease' ./...

release-repro:
	GOWORK=off go run -mod=readonly ./cmd/snapref-release --verify-repro

release-dist: release-check release-repro
	GOWORK=off go run -mod=readonly ./cmd/snapref-release --out dist
