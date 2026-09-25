package generate

// Default images for MyLab-managed databases (SPEC.md §6.8), used when a
// package doesn't override them. Kept here so they can be changed in one
// place.
//
// TODO: pin by digest before release (SPEC.md §6.10). The Postgres major
// version is a placeholder: packages should normally choose their own.
const (
	DefaultPostgresImage = "docker.io/library/postgres:17-alpine"
	DefaultRedisImage    = "docker.io/valkey/valkey:8-alpine"
)
