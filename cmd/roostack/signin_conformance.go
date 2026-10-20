//go:build ssoconformance

package main

import (
	"os"
	"strings"

	"roostack.cloud/roostack/internal/sso"
)

// A build for running the OpenID Foundation's conformance suite against the
// sign-in page (SPEC.md §5.2, docs/SSO-CONFORMANCE.md), and nothing else:
// `go build -tags ssoconformance`. Release builds never have this file.
//
// The suite signs in as a registered app, so it needs its own address to be
// one of that app's redirect addresses. ROOSTACK_SSO_CONFORMANCE_REDIRECTS
// (comma-separated) adds those addresses to every app with single sign-on
// through OIDC.
func conformanceClients(clients func() []sso.Client) func() []sso.Client {
	var extra []string
	for _, u := range strings.Split(os.Getenv("ROOSTACK_SSO_CONFORMANCE_REDIRECTS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			extra = append(extra, u)
		}
	}
	return func() []sso.Client {
		list := clients()
		out := make([]sso.Client, 0, len(list))
		for _, c := range list {
			if len(c.RedirectURIs) > 0 {
				c.RedirectURIs = append(append([]string{}, c.RedirectURIs...), extra...)
			}
			out = append(out, c)
		}
		return out
	}
}
