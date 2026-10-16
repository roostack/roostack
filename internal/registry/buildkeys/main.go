// Command buildkeys prints which registry root keys a build of Roostack
// trusts (SPEC.md §8.3.3). make build runs it with the binaries' linker
// flags and writes what it prints to bin/registry-keys, and
// scripts/build-deb.sh refuses to package a release without the
// registry's real root key or with a development one. It reads the
// compiled-in keys rather than searching the source, so a key in a
// comment doesn't count, and a malformed one fails the build.
package main

import (
	"fmt"
	"os"

	"roostack.cloud/roostack/internal/registry"
)

func main() {
	if _, err := registry.RootKeys(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	real, dev := registry.Trusted()
	fmt.Printf("root_keys=%d\ndev_key=%t\n", real, dev)
}
