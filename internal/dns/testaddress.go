package dns

import "roostack.cloud/roostack/internal/config"

// testAddressProvider is a test address's (SPEC.md §6.6, "A test address
// for throwaway labs"): a name under .localhost, which only `roostack
// test-address` sets. Nothing is asked of DNS, since every name under
// .localhost already means the server itself, and Caddy signs the
// certificates with its own certificate authority. It's never offered in
// the list, and config.Lab.Validate allows it only for a .localhost name.
var testAddressProvider = &Provider{
	ID: config.ProviderTestAddress, Title: "A test address", Hidden: true,
	directive: func([]string) string { return "issuer internal" },
}
