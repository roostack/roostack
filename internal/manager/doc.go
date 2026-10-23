// Package manager is the controller's business logic: everything the
// pages ask for that isn't a single server's own work. HTTP handlers stay
// thin (SPEC.md §6.3) and call it; a server's own work goes through
// node.Node. Manager holds the lab's desired state (lab.yaml and the
// instance files), builds each server's assignment from it and hands it
// over (servers.go).
//
// The files, by feature:
//
// Services:
//   - manager.go: the Manager type, the catalog, installing and removing,
//     the install summary, package files and icons, ports;
//   - local.go: the Owner's own packages and the package policy (SPEC.md
//     §8.3.4); withdrawals.go: the registry's withdrawals and stops;
//   - update.go: updating to newer package versions (SPEC.md §7.1);
//   - retry.go: running install hooks again with changed settings;
//   - public.go, edge.go: public addresses, the web application
//     firewall, a lab's edge and Roostack Cloud's relay (SPEC.md §6.11,
//     §10.5);
//   - sso.go: single sign-on for each service (SPEC.md §5.2);
//   - import.go: switching a service to data brought in (SPEC.md §8.4);
//   - photos.go: photo migration into Immich (SPEC.md §8.5);
//   - libraries.go: media libraries (SPEC.md §8.1);
//   - monitor.go: monitoring's checks and the status page (SPEC.md §6.15).
//
// Servers:
//   - servers.go: the lab's servers, building and applying assignments,
//     and each service's state;
//   - arch.go: servers' processors and packages without a build for them;
//   - links.go: direct WireGuard links between servers;
//   - location.go: where each server is, at home or away.
//
// Backups (SPEC.md §6.9, §10.10):
//   - backups.go: the main backup's destination, S3 providers and pruning;
//   - copies.go, plan.go: extra backups, the 3-2-1 checks and what a
//     plan lacks;
//   - destination.go, s3check.go, accountkey.go: what a destination's
//     credentials can do, a bucket's versioning and erase protection, and
//     a new key for an online storage account;
//   - rollback.go: rolling a server's versioned bucket back;
//   - quarantine.go: junk found in backup repositories;
//   - keychange.go: changing the recovery key;
//   - cloudreport.go: which servers back up to Roostack Cloud.
//
// Moving and restoring (SPEC.md §10.10):
//   - transfer.go: transferJob, the steps a move and a restore share;
//   - restore.go: restoring a service from any backup onto any server;
//   - move.go: moving a service to another server, and the data kept;
//   - lost.go: a lost server, and restoring its services elsewhere;
//   - hold.go: a service held stopped after a restore or move whose
//     databases didn't load (SPEC.md §6.9).
//
// Storage for other labs (SPEC.md §10.10):
//   - guests.go, guesthost.go: the labs that store backups here, and
//     which server holds them.
package manager
