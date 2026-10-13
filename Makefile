# Common development tasks. Everything is plain `go`; this just saves typing.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build help test golden vet caddy-check check-images

# Linux binaries for the machine's own architecture, or another with
# GOARCH=arm64 (or amd64).
build: help
	GOOS=linux CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...
	scripts/copyright.sh > bin/copyright

# The help site (SPEC.md §6.3.1), built with Zensical and embedded in the
# binary from internal/web/helpsite/site. Strict: a broken link fails the
# build. Without uv, Roostack is built without it (and says so at /help/).
help:
	@if command -v uv >/dev/null; then \
		cd help && uv run zensical build --strict && cd .. && \
		rm -rf internal/web/helpsite/site && cp -r help/site internal/web/helpsite/site; \
	else \
		echo "warning: uv isn't installed, so the help site isn't built into Roostack" >&2; \
	fi

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

# Check every pinned image has amd64 and arm64 builds, by asking the
# registries (ARM64-PLAN.md 1.3). Run after changing a pin.
check-images:
	ROOSTACK_IMAGE_CHECK=1 go test -count=1 -run TestImagesMultiArch -v ./internal/generate/
