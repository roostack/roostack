// Package node is one server in the lab (SPEC.md §10). The Node interface
// is everything the controller asks of a server; Local is this server and
// Remote is another one, reached over the lab API (api.go serves it on
// every server, remote.go calls it). Both implement Node, so the manager
// and the web layer don't need to know which they have. What a server may
// not have (its backups, moving data, its disks...) is a Node method that
// returns nil when it doesn't (Node's doc says more).
//
// A server's work is given to it as an Assignment: the services placed
// on it and the lab settings they need. The controller pushes each
// server its assignment whenever the desired state changes, and Local
// applies it in the background, one apply at a time (generate, then
// reconcile's plan and apply), then runs the package's install hooks
// (SPEC.md §10.3, ARCHITECTURE.md "Life of an install").
//
// Where things are:
//   - local.go, wire.go: Node, Local, Assignment and its wire form;
//     api.go, remote.go, fleet.go: the lab API's route table, its
//     client, and the controller's set of other servers; *wire.go: each
//     feature's client methods beside the routes that answer them
//     (roundtrip_test.go calls every one);
//   - install.go, hooks.go, ssohooks.go, build.go: an install's steps,
//     package hooks, single sign-on's hooks and Caddy's image build;
//   - maintenance.go, settings.go: the server's maintenance jobs (backups,
//     restores, OS updates, restarts) and its own settings (time zone,
//     SSH, backup destination, restart policy);
//   - details.go, disk.go, memory.go, arch.go, leftdata.go: what the pages
//     show about the server and its services;
//   - history.go: finished activity logs kept as history (SPEC.md §6.14);
//   - move.go, update.go: a server's side of moving and updating a
//     service;
//   - kept.go: the data set aside on the server, one list whatever set it
//     aside (internal/kept);
//   - import*.go: bringing data in from the person's own copy (SPEC.md
//     §8.4);
//   - photos.go: running immich-go for photo migration (SPEC.md §8.5),
//     localPhotos;
//   - media.go, libraries.go, libwatch.go: drives, shares and media
//     libraries (SPEC.md §8.1);
//   - guesthost.go: storage for other labs.
package node
