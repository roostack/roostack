// Package setup is the logic behind the first-run wizard (SPEC.md §6.3.2):
// it checks and saves what the Owner enters, step by step, and records how
// far they've got, so the web layer only shows the forms. The wizard's
// steps are where the lab is, at home or on the internet (network.go,
// SPEC.md §6.12), the time zone, SSH access, the lab's address and its DNS
// provider's login (tested before it's saved) or a roost.ac name, the
// backup destination, including readying a drive through the agent, and
// the recovery key. Settings reuses the same functions to change these
// later.
//
// Progress is kept in the store's runtime settings (setup.*); the
// settings themselves go to lab.yaml and, for secrets, Podman secrets.
// Mistakes people can fix come back as usererr.Error, whose message the
// pages show as it is.
package setup
