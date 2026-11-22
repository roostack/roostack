package labnet

// A server serving directly relays its Caddy's walls to the controller's
// sign-in page (SPEC.md §5.2, §10.5): the check for each request to a
// walled service, and the sign-in's hand-off on the service's address.

// PathWall is where the controller takes them, followed by the sign-in
// page's own path (its check, or the hand-off under the service's
// address).
const PathWall = "/v1/wall"

// WallHostHeader carries the walled service's address (the request's
// Host as its server's Caddy sent it), since the request to the
// controller is addressed to the lab API.
const WallHostHeader = "X-Roostack-Wall-Host"

// A server serving its private routes at home relays its Caddy's DNS
// challenges to the controller (SPEC.md §10.5, "Serving directly"): its
// Caddy speaks acme-dns, whose update names the record by a subdomain,
// which is the name the certificate is for.

// PathChallenge is where the controller takes them.
const PathChallenge = "/v1/challenge"

// ChallengeUpdate is acme-dns's update: the name, and the TXT value.
type ChallengeUpdate struct {
	Subdomain string `json:"subdomain"`
	TXT       string `json:"txt"`
}
