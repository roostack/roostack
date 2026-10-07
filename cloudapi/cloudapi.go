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
	PathLab        = "/v1/lab"          // GET → LabInfo; DELETE unlinks (mutual TLS)
	PathLabRenew   = "/v1/lab/renew"    // POST RenewRequest → RenewResponse (mutual TLS)
	PathLabName    = "/v1/lab/name"     // POST NameRequest → NameClaim (mutual TLS)
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
type NameAvailability struct {
	Available bool `json:"available"`
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
