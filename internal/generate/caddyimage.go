package generate

import "cmp"

// CaddyImage is Roostack's Caddy image (build/caddy/Containerfile: upstream
// Caddy with the plugins Roostack uses), built for amd64 and arm64 by the
// Caddy image workflow (.github/workflows/caddy-image.yml,
// scripts/build-caddy-image.sh) and published on GHCR. It's named by
// digest, so a release's signature covers it and Podman refuses any other
// image (SPEC.md §6.5). Servers download it; none builds it.
var CaddyImage = cmp.Or(devCaddyImage, PublishedCaddyImage)

// PublishedCaddyImage is the image the workflow printed for the
// Containerfile whose SHA-256 is CaddyContainerfileSHA256 (a test checks
// that they go together, and the release workflow that the image is
// published, for both processors, with that Containerfile's label). After
// changing the Containerfile, run the workflow and put both here. The
// digest is zeros until the workflow first runs: a release refuses it, and
// a development build needs CADDY_DEV_IMAGE meanwhile.
const (
	PublishedCaddyImage      = "ghcr.io/roostack/roostack-caddy@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	CaddyContainerfileSHA256 = "7fe5fa774d6d60c1adbd5c593adb3b47cefbd02c77af6aca9f49f8d59c8b477c"
)

// CaddyContainerfileLabel is the image's label holding the SHA-256 of the
// Containerfile it was built from (scripts/build-caddy-image.sh).
const CaddyContainerfileLabel = "io.roostack.containerfile.sha256"

// devCaddyImage is another image for a development build (make build
// CADDY_DEV_IMAGE=…, set with -ldflags -X): one built from a changed
// Containerfile, or before the workflow has published one. A release never
// has one (scripts/build-release.sh refuses).
var devCaddyImage string
