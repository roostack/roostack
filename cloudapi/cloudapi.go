// Package cloudapi is the API between a Roostack lab and Roostack Cloud
// (roost.ac): the paths and bodies both sides use, so they can't drift.
// Roostack's client is internal/cloud; Cloud implements the server. Only
// what works standalone stays in Roostack: this package carries no Cloud
// logic, only the contract (SPEC.md §6.6).
package cloudapi

import "time"

// The API labs talk to. Paths are relative to Cloud's address
// (Roostack's config.Cloud.Address). Bodies are JSON. Errors are
// ErrorBody with a status code; 401 or 403 on a lab's own endpoints
// (mutual TLS, the lab's certificate) means Cloud doesn't know the
// certificate (any more).
const (
	PathLinks      = "/v1/links"        // POST LinkRequest → LinkStarted
	PathLinkStatus = "/v1/links/status" // POST LinkSecret → LinkStatus
	PathLinkCancel = "/v1/links/cancel" // POST LinkSecret
	PathNames      = "/v1/names/"       // GET <name> → NameAvailability
	PathInfo       = "/v1/info"         // GET → Info
	PathLab        = "/v1/lab"          // GET → LabInfo; DELETE unlinks (mutual TLS)
	PathLabRenew   = "/v1/lab/renew"    // POST RenewRequest → RenewResponse (mutual TLS)
	PathLabName    = "/v1/lab/name"     // POST NameRequest → NameClaim (mutual TLS)
	PathLabRelay   = "/v1/lab/relay"    // PUT RelayRequest → Relay; DELETE stops relaying (mutual TLS)
	// PUT RelayBans (mutual TLS); 404 while the lab isn't relayed.
	PathLabRelayBans = "/v1/lab/relay/bans"
	// GET ?days=N[&by=day] → RelayStats (mutual TLS).
	PathLabRelayStats = "/v1/lab/relay/stats"
)

// A claimed name's records, at the addresses in its NameClaim's
// credentials, authenticated with its acme-dns account (the headers
// HeaderUser and HeaderKey, as acme-dns uses):
//
//   - server_url + "/update": acme-dns's own API, which Caddy's acmedns
//     module calls: POST {"subdomain": …, "txt": …} sets the name's
//     challenge record (acme-dns keeps the latest two values).
//   - records_url + "/<name>/<type>": Cloud's records API, for A records
//     inside the name only (<name> relative to it, "@" for the name
//     itself; no public wildcard). GET → RecordValues; PUT RecordValue.
const (
	HeaderUser = "X-Api-User"
	HeaderKey  = "X-Api-Key"
)

// Credential fields in NameClaim.Credentials (they're also the fields of
// Roostack's roostack-cloud DNS provider).
const (
	CredServerURL  = "server_url"
	CredUsername   = "username"
	CredPassword   = "password"
	CredSubdomain  = "subdomain"
	CredRecordsURL = "records_url"
)

// RecordValues is a record's current values (none if it doesn't exist).
type RecordValues struct {
	Values []string `json:"values"`
}

// RecordValue sets a record to one value.
type RecordValue struct {
	Value string `json:"value"`
}

// ACMEUpdate is acme-dns's /update request.
type ACMEUpdate struct {
	Subdomain string `json:"subdomain"`
	TXT       string `json:"txt"`
}

// LinkRequest starts linking: what the lab tells Cloud about itself.
type LinkRequest struct {
	LabName string `json:"lab_name"`
	// Name is the roost.ac name it asks for ("" for none).
	Name    string `json:"name,omitempty"`
	Version string `json:"version"`
	// CSR is a certificate request (DER) for the key the lab will use.
	CSR []byte `json:"csr"`
	// ReturnURL is the lab's page to send the browser back to, once the
	// link is confirmed or cancelled (the address the browser uses for the
	// lab); "" for none. Cloud shows it before going there.
	ReturnURL string `json:"return_url,omitempty"`
}

// LinkStarted is Cloud's answer: the code people compare, the page to
// open, and the secret only the lab holds, to ask how it's going.
type LinkStarted struct {
	Code    string    `json:"code"`
	OpenURL string    `json:"open_url"`
	Secret  string    `json:"secret"`
	Expires time.Time `json:"expires"`
}

// LinkSecret names a link being made.
type LinkSecret struct {
	Secret string `json:"secret"`
}

// Link states (LinkStatus.State).
const (
	StateWaiting = "waiting" // not confirmed yet
	StateLinked  = "linked"  // confirmed: Certificate is set
	StateExpired = "expired" // expired or cancelled
)

// LinkStatus is how linking is going.
type LinkStatus struct {
	State string `json:"state"`
	// Certificate (PEM) is the lab's certificate, once linked.
	Certificate string `json:"certificate,omitempty"`
	LabInfo
}

// LabInfo is what Cloud says about a linked lab.
type LabInfo struct {
	// LabID is Cloud's name for the lab.
	LabID string `json:"lab_id"`
	// Account is how the account is shown to the lab (e.g. its email
	// address, partly hidden).
	Account string `json:"account"`
}

// NameAvailability says whether a roost.ac name can be had.
// RelayRequest asks Cloud to bring public visitors for the lab's names
// to it through the relay: its WireGuard key on the relay network, and
// its public names (in full, each the lab's roost.ac name or one under
// it). Asking again replaces all of it.
type RelayRequest struct {
	PublicKey string   `json:"public_key"`
	Hosts     []string `json:"hosts"`
	// Countries is who the edges let in by country (none set: anyone).
	Countries RelayCountries `json:"countries"`
	// TimeZone is the lab's (IANA, e.g. Australia/Sydney): its statistics'
	// days run midnight to midnight there. Empty is UTC.
	TimeZone string `json:"time_zone,omitempty"`
}

// RelayCountries limits the lab's visitors at the edges by country, with
// ISO 3166 codes (AU): only those in Allow, or everyone but those in
// Block, never both. OpenUntil lets everyone in until then (travel);
// after it, the limit is back by itself.
type RelayCountries struct {
	Allow     []string  `json:"allow,omitempty"`
	Block     []string  `json:"block,omitempty"`
	OpenUntil time.Time `json:"open_until,omitzero"`
}

// RelayBans are the addresses and ranges the lab's CrowdSec has banned
// (at most MaxRelayBans), refused at the edges for this lab only. Each
// replaces those sent before; the edges drop them an hour after the
// last, so a lab gone quiet doesn't keep stale bans.
type RelayBans struct {
	Bans []string `json:"bans"`
}

// RelayStats are the lab's statistics through the relay, oldest first:
// by the hour (the last 31 days at most), or by the day in the lab's time
// zone (by=day, up to 400). Counts only: nothing about any visitor is
// kept. Visitors are different addresses (an IPv4 address, or an IPv6
// /64) in the period: someone who visits all day counts once that day.
type RelayStats struct {
	Periods []RelayPeriod `json:"periods"`
}

// RelayPeriod is an hour's or a day's statistics. A day has Day
// (YYYY-MM-DD in the lab's time zone) and Start at its midnight there.
// Bytes are only counted by the day (BytesIn from visitors, BytesOut to
// them), attributed to the hour they were sent in. Refused counts
// connections turned away for the lab, by why ("banned" by its CrowdSec,
// "country").
type RelayPeriod struct {
	Start       time.Time        `json:"start"`
	Day         string           `json:"day,omitempty"`
	Connections int64            `json:"connections"`
	Visitors    int64            `json:"visitors"`
	Countries   map[string]int64 `json:"countries,omitempty"`
	Refused     map[string]int64 `json:"refused,omitempty"`
	BytesIn     int64            `json:"bytes_in,omitempty"`
	BytesOut    int64            `json:"bytes_out,omitempty"`
}

// MaxRelayBans is the most bans a lab sends (its newest, if it has more).
const MaxRelayBans = 1000

// Relay is how the lab joins the relay: its address on the relay network
// (connections from the edges come to it there), and the edges to dial.
type Relay struct {
	Addr string `json:"addr"`
	// Prefix is the relay network's prefix length (the lab's interface).
	Prefix int         `json:"prefix"`
	Edges  []RelayEdge `json:"edges"`
}

// RelayEdge is one of Cloud's edges, as a lab dials it.
type RelayEdge struct {
	Name string `json:"name"`
	// Endpoint is its WireGuard (host:port), and PublicKey its key.
	Endpoint  string `json:"endpoint"`
	PublicKey string `json:"public_key"`
	// Addr is its address on the relay network, where its connections to
	// the lab come from: the only source whose PROXY header the lab
	// believes.
	Addr string `json:"addr"`
	// PublicIPv4 is where visitors reach it (public names point there).
	PublicIPv4 string `json:"public_ipv4"`
}

// Info is about Cloud itself, for a lab's pages before it asks anything
// else: the suffix its names have (roost.ac, or a test Cloud's own).
type Info struct {
	NameSuffix string `json:"name_suffix"`
}

// DefaultNameSuffix is Roostack Cloud's, until a Cloud says otherwise.
const DefaultNameSuffix = "roost.ac"

type NameAvailability struct {
	Available bool `json:"available"`
	// Held: not available, but held for the account that had it (after
	// its lab was unlinked): that account can have it again, so a lab may
	// link and then claim it (only Cloud, once linked, knows whose).
	Held bool `json:"held,omitempty"`
	// Reason, when it isn't, is a sentence for people.
	Reason string `json:"reason,omitempty"`
}

// RenewRequest asks for a new certificate, for a new key.
type RenewRequest struct {
	CSR []byte `json:"csr"`
}

// RenewResponse is the new certificate (PEM).
type RenewResponse struct {
	Certificate string `json:"certificate"`
}

// ErrorBody is an error's body: a sentence for people.
type ErrorBody struct {
	Error string `json:"error"`
}

// NameRequest claims a roost.ac name for the lab.
type NameRequest struct {
	Name string `json:"name"`
}

// NameClaim is the lab's name and the credentials that write its records
// (SPEC.md §6.6): Name is the full name (e.g. smiths.roost.ac);
// Credentials are the fields of the roostack-cloud DNS provider
// (internal/dns): an acme-dns account for its challenge record
// (server_url, username, password, subdomain) and records_url, Cloud's
// records API, which takes the same account.
type NameClaim struct {
	Name        string            `json:"name"`
	Credentials map[string]string `json:"credentials"`
}
