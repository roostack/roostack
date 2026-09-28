package generate

// Default images for Roostack-managed databases (SPEC.md §6.8), used when a
// package doesn't override them. Kept here so they can be changed in one
// place.
//
// Digests looked up 2026-09-26. The Postgres major version is a default
// only: packages should normally choose their own.
const (
	DefaultPostgresImage = "docker.io/library/postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"
	DefaultRedisImage    = "docker.io/valkey/valkey:8-alpine@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
)
