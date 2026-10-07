// Package cloud is the lab's side of Roostack Cloud (SPEC.md §6.6,
// "Linking a lab to Roostack Cloud"): linking the lab to a Cloud account,
// the lab's certificate for talking to Cloud, and unlinking.
//
// Only the controller talks to Cloud. Every request after linking uses
// mutual TLS with the lab's certificate, whose key never leaves the
// server.
package cloud

import "time"

// The API the lab talks to. Paths are relative to Cloud's address
// (config.Cloud.Address). Errors are JSON {"error": "…"} with a status
// code; 401 or 403 on a lab's own endpoints means Cloud doesn't know the
// lab's certificate (any more).
const (
	pathLinks      = "/v1/links"        // POST LinkRequest → LinkStarted
	pathLinkStatus = "/v1/links/status" // POST LinkSecret → LinkStatus
	pathLinkCancel = "/v1/links/cancel" // POST LinkSecret
	pathNames      = "/v1/names/"       // GET <name> → NameAvailability
	pathLab        = "/v1/lab"          // GET → LabInfo; DELETE unlinks (mutual TLS)
	pathLabRenew   = "/v1/lab/renew"    // POST RenewRequest → RenewResponse (mutual TLS)
	pathLabName    = "/v1/lab/name"     // POST NameRequest → NameClaim (mutual TLS)
)

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

// apiError is an error body.
type apiError struct {
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
