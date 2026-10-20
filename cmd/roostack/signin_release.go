//go:build !ssoconformance

package main

import "roostack.cloud/roostack/internal/sso"

// conformanceClients is the apps as they are: only a build for the
// conformance suite adds its addresses (signin_conformance.go).
func conformanceClients(clients func() []sso.Client) func() []sso.Client { return clients }
