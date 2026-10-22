# Common development tasks. Everything is plain `go`; this just saves typing.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Reproducible builds (docs/RELEASING.md): -trimpath leaves out the folder
# the code was built in, and an empty build ID leaves out the go tool's
# own cache keys, so the same commit and Go build the same binary
# anywhere.
LDFLAGS := -buildid= -X main.version=$(VERSION)
# A development root key for the service registry (SPEC.md §8.3.3), to
# fetch from a registry signed with test keys: never for a release
# (docs/REGISTRY-KEYS.md).
ifneq ($(REGISTRY_DEV_KEY),)
LDFLAGS += -X roostack.cloud/roostack/internal/registry.devRootKey=$(REGISTRY_DEV_KEY)
endif

.PHONY: build help release verify-release test editor-test golden generated vet caddy-check check-images

# Linux binaries for the machine's own architecture, or another with
# GOARCH=arm64 (or amd64).
build: help
	GOOS=linux CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...
	scripts/copyright.sh > bin/copyright
	@# Which registry root keys the build trusts, compiled with the same
	@# flags for this machine, for build-deb.sh to check before making a
	@# release (SPEC.md §8.3.3).
	GOOS= GOARCH= go run -ldflags "$(LDFLAGS)" ./internal/registry/buildkeys > bin/registry-keys
	@rm -f bin/registry-dev-key

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

# A release, from a checkout of its tag: both packages, both plain
# binaries and SHA256SUMS in dist/release (scripts/build-release.sh,
# docs/RELEASING.md). The release workflow runs the same, and so can
# anyone checking that a release was built from its source.
release:
	scripts/build-release.sh

# Checks a published release against its source: its signature, then a
# rebuild from its tag in the release's own image, file by file
# (scripts/verify-release.sh, docs/RELEASING.md). make verify-release VERSION=1.2.3
verify-release:
	scripts/verify-release.sh "$(VERSION)"

test: editor-test
	go test ./...

# The package editor's JavaScript tests, with Node 20 or later: its checks
# against pkgspec's on every package of the corpus, the Compose import and
# the suggestions (SPEC.md §8.3.7). No packages to install.
editor-test:
	node --test internal/editor/test/*.test.js

# Rewrite generator golden files after an intended output change; review the diff.
golden:
	go test ./internal/generate/ -update

# Rewrite every file Go generates (CONTRIBUTING.md, "Regenerating files"):
# the golden files, the package editor's schema, examples and corpus, the
# design system's gallery and THIRD_PARTY.md. Review the diff.
generated: golden
	go test ./internal/editor -update-editor
	go test ./internal/web -run 'TestGalleryFile|TestThirdPartyFile' -update-gallery -update-credits

# gofmt over the committed Go files only, so another worktree's work in
# progress under .claude/worktrees doesn't fail this checkout's.
vet:
	go vet ./...
	test -z "$$(git ls-files -z '*.go' | xargs -0 gofmt -l)"

# Check generated Caddy config against a real Caddy build (see internal/caddy).
caddy-check:
	test -n "$(ROOSTACK_CADDY_BIN)" || (echo "set ROOSTACK_CADDY_BIN" && exit 1)
	ROOSTACK_CADDY_BIN=$(ROOSTACK_CADDY_BIN) go test -run TestCaddyAdapt -v ./internal/caddy/

# Check every pinned image has amd64 and arm64 builds, by asking the
# registries (ARM64-PLAN.md 1.3). Run after changing a pin.
check-images:
	ROOSTACK_IMAGE_CHECK=1 go test -count=1 -run TestImagesMultiArch -v ./internal/generate/
