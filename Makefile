# Common development tasks. Everything is plain `go`; this just saves typing.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test golden vet caddy-check

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...
	scripts/copyright.sh > bin/copyright

test:
	go test ./...

# Rewrite generator golden files after an intended output change; review the diff.
golden:
	go test ./internal/generate/ -update

vet:
	go vet ./...
	test -z "$$(gofmt -l .)"

# Check generated Caddy config against a real Caddy build (see internal/caddy).
caddy-check:
	test -n "$(ROOSTACK_CADDY_BIN)" || (echo "set ROOSTACK_CADDY_BIN" && exit 1)
	ROOSTACK_CADDY_BIN=$(ROOSTACK_CADDY_BIN) go test -run TestCaddyAdapt -v ./internal/caddy/
