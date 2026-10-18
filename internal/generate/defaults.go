package generate

// Default images for Roostack-managed databases (SPEC.md §6.8), used when a
// package doesn't override them. Kept here so they can be changed in one
// place.
//
// Digests looked up 2026-09-26 (MySQL and MariaDB 2026-10-19). The Postgres
// major version is a default only: packages should normally choose their
// own. MySQL and MariaDB are their long-term-support lines (the Owner,
// 2026-10-19): MySQL 8.4 (to April 2032; Ghost supports 8.0 and 8.4 only)
// and MariaDB 11.8 (to June 2028). Moving a pin to a newer major version
// relies on the servers upgrading their data as they start (ASSUMPTIONS
// I3).
const (
	DefaultMySQLImage   = "docker.io/library/mysql:8.4.11@sha256:24989179f58856db763f418010c1169fecc944e3ec1ea7e921572d83eb070f70"
	DefaultMariaDBImage = "docker.io/library/mariadb:11.8.9@sha256:f16d7a344413e519dff518f27bdb221388f84b0d274c97c4fb5f2cb0dc1b34fa"

	DefaultPostgresImage = "docker.io/library/postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"
	DefaultRedisImage    = "docker.io/valkey/valkey:8-alpine@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
)
