// Package cloud is the controller's link to Roostack Cloud (SPEC.md §6.6).
// A lab works without Cloud; this package is what it uses once the Owner
// links it, and only on the controller. Service holds the link and
// everything that goes over it:
//
//   - link.go: linking with a link code and Cloud's page, the lab's client
//     certificate for mutual TLS (renewed with a new key), the daily
//     check-in, and unlinking;
//   - report.go: what the lab tells Cloud after each check-in;
//   - check.go: claiming a roost.ac name and checking its DNS proof (the
//     lab's DNS provider is then Roostack Cloud, internal/dns/cloud.go);
//   - relay.go: Cloud's relay for public services, and the bans sent to
//     its edges (SPEC.md §10.5);
//   - storage.go: Roostack Cloud backup's keys and the lab's use of its
//     space (SPEC.md §6.9);
//   - recovery.go: the recovery identity, registered with Cloud, used to
//     restore from Cloud and to link again;
//   - requestlog.go: the log of each request and what it sent, with
//     secrets left out by an allowlist a test keeps complete;
//   - client.go: the HTTP client and Cloud's errors.
//
// Everything is kept in <state>/cloud/ (Dir). The contract with Cloud is
// the public cloudapi package, which Cloud's own repository imports too;
// cloudtest fakes Cloud's side for Roostack's tests.
package cloud
