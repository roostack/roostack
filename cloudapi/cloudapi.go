// Package cloudapi is the API between a Roostack lab and Roostack Cloud
// (api.roostack.cloud): the paths and bodies both sides use, so they can't drift.
// Roostack's client is internal/cloud; Cloud implements the server. Only
// what works standalone stays in Roostack: this package carries no Cloud
// logic, only the contract (SPEC.md §6.6).
package cloudapi

import (
	"strings"
	"time"
)

// The API labs talk to. Paths are relative to Cloud's address
// (Roostack's config.Cloud.Address). Bodies are JSON. Errors are
// ErrorBody with a status code. On a lab's own endpoints (mutual TLS, the
// lab's certificate), only 401 means Cloud doesn't know the certificate
// (any more), and the lab then relinks or forgets the link. 403 refuses
// one request, and 423 Locked every request while the account is
// suspended: the lab stays linked and shows ErrorBody's message. 429 asks
// it to slow down.
const (
	PathLinks      = "/v1/links"        // POST LinkRequest → LinkStarted
	PathLinkStatus = "/v1/links/status" // POST LinkSecret → LinkStatus
	PathLinkCancel = "/v1/links/cancel" // POST LinkSecret
	PathNames      = "/v1/names/"       // GET <name> → NameAvailability
	PathInfo       = "/v1/info"         // GET → Info
	PathLab        = "/v1/lab"          // GET → LabInfo; DELETE unlinks (mutual TLS)
	PathLabRenew   = "/v1/lab/renew"    // POST RenewRequest → RenewResponse (mutual TLS)
	PathLabName    = "/v1/lab/name"     // POST NameRequest → NameClaim (mutual TLS)
	// POST NameRelease → NameReleased (mutual TLS): the lab is done with
	// a name it kept (NameRequest.KeepEarlier), or gives back the new one
	// (its earlier name is in use again). A Cloud from before it answers
	// 404: it released the earlier name with the claim.
	PathLabNameRelease = "/v1/lab/name/release"
	PathLabRelay       = "/v1/lab/relay" // PUT RelayRequest → Relay; GET → Relay; DELETE stops relaying (mutual TLS)
	// PUT RelayBans (mutual TLS); 404 while the lab isn't relayed.
	PathLabRelayBans = "/v1/lab/relay/bans"
	// GET ?days=N[&by=day] → RelayStats (mutual TLS).
	PathLabRelayStats = "/v1/lab/relay/stats"
	// POST StorageRequest → StorageKeys (mutual TLS): Roostack Cloud
	// backup's short-lived keys. GET PathLabStorage → StorageUsage.
	// While the account is suspended, keys are given as far as its
	// suspension allows (all of them, only for restores, or none), and
	// the rest answered 423.
	PathLabStorageKeys = "/v1/lab/storage/keys"
	PathLabStorage     = "/v1/lab/storage"
	// PUT RecoveryIdentity (mutual TLS): the lab's recovery identity,
	// replacing the one before (a new recovery key).
	PathLabRecoveryIdentity = "/v1/lab/recovery-identity"
	// PUT LabReport (mutual TLS), after each check-in: what the lab
	// tells Cloud about itself. A Cloud from before it answers 404, and
	// the lab carries on without it.
	PathLabReport = "/v1/lab/report"
	// GET → News (mutual TLS), every few minutes from a linked lab that
	// isn't relayed (a relayed one hears the same in Relay). A Cloud
	// from before it answers 404, and the lab asks again a day later.
	PathLabNews = "/v1/lab/news"

	// Proving the recovery key, for a lab that has lost its controller
	// (no certificate): POST RestoreChallengeRequest → RestoreChallenge,
	// then one of POST RestoreProof → RestoreAccess (keys to read the
	// backups of every lab with the key) or POST RelinkRequest → Relinked
	// (a new certificate for the linked one). While the lab's account is
	// suspended, access follows its suspension as PathLabStorageKeys's
	// restore keys do, and relinking is answered 423.
	PathRestoreChallenge = "/v1/restore/challenge"
	PathRestoreAccess    = "/v1/restore/access"
	PathRestoreRelink    = "/v1/restore/relink"
)

// Route is one of the API's routes: Method and Path as http.ServeMux
// takes them (Pattern), PathNames's with its {name}. Lab says it's one
// of the lab's own (mutual TLS: the lab's certificate is needed, and 401
// means Cloud doesn't accept it). Only a lab's own route has the other
// two: WhileSuspended, a suspended account's labs may still use it
// (otherwise 423 Locked); Writes, it changes something, so it counts
// against the lab's limit on changes (429 past it, checked before the
// suspension).
type Route struct {
	Method, Path   string
	Lab            bool
	WhileSuspended bool
	Writes         bool
}

// Pattern is the route as http.ServeMux takes it ("GET /v1/lab").
func (r Route) Pattern() string { return r.Method + " " + r.Path }

// Routes are every route of the API, as Cloud serves them; Roostack's
// fake Cloud (internal/cloud/cloudtest) follows them, and Cloud's own
// tests check its routes against them. The acme-dns and records APIs
// (HeaderUser) are at the addresses a NameClaim gives, not here.
var Routes = []Route{
	{Method: "POST", Path: PathLinks},
	{Method: "POST", Path: PathLinkStatus},
	{Method: "POST", Path: PathLinkCancel},
	{Method: "GET", Path: PathNames + "{name}"},
	{Method: "GET", Path: PathInfo},
	// A suspended account's lab may still check in, unlink, renew its
	// certificate (so a suspension never makes it forget the link), report,
	// and use its backups as far as the suspension allows.
	{Method: "GET", Path: PathLab, Lab: true, WhileSuspended: true},
	{Method: "DELETE", Path: PathLab, Lab: true, WhileSuspended: true},
	{Method: "POST", Path: PathLabRenew, Lab: true, WhileSuspended: true, Writes: true},
	{Method: "POST", Path: PathLabName, Lab: true, Writes: true},
	{Method: "POST", Path: PathLabNameRelease, Lab: true, Writes: true},
	{Method: "PUT", Path: PathLabRelay, Lab: true, Writes: true},
	{Method: "GET", Path: PathLabRelay, Lab: true},
	{Method: "DELETE", Path: PathLabRelay, Lab: true, Writes: true},
	{Method: "PUT", Path: PathLabRelayBans, Lab: true, Writes: true},
	{Method: "GET", Path: PathLabRelayStats, Lab: true},
	{Method: "POST", Path: PathLabStorageKeys, Lab: true, WhileSuspended: true, Writes: true},
	{Method: "GET", Path: PathLabStorage, Lab: true, WhileSuspended: true},
	{Method: "PUT", Path: PathLabRecoveryIdentity, Lab: true, Writes: true},
	{Method: "PUT", Path: PathLabReport, Lab: true, WhileSuspended: true, Writes: true},
	{Method: "GET", Path: PathLabNews, Lab: true, WhileSuspended: true},
	{Method: "POST", Path: PathRestoreChallenge},
	{Method: "POST", Path: PathRestoreAccess},
	{Method: "POST", Path: PathRestoreRelink},
}

// RouteOf is the route a request's method and path (no query) take, if
// any.
func RouteOf(method, path string) (Route, bool) {
	for _, r := range Routes {
		if r.Method != method {
			continue
		}
		if prefix, ok := strings.CutSuffix(r.Path, "{name}"); ok {
			if name, ok := strings.CutPrefix(path, prefix); ok && name != "" && !strings.Contains(name, "/") {
				return r, true
			}
		} else if r.Path == path {
			return r, true
		}
	}
	return Route{}, false
}

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
	// Suspended is why the lab's account is suspended, a sentence for
	// people ("": it isn't, or a Cloud from before it doesn't say).
	Suspended string `json:"suspended,omitempty"`
	// Names are the lab's names that don't work because of Cloud: waiting
	// for approval, refused, or suspended (none: every name works, or a
	// Cloud from before it doesn't say).
	Names []NameStatus `json:"names,omitempty"`
	// Recipients are the addresses of the lab's last report (LabReport.
	// Emails), each with whether it gets Cloud's emails about the lab:
	// each confirms first, from a link Cloud emails it (none: the lab
	// hasn't reported, or a Cloud from before it doesn't say).
	Recipients []RecipientStatus `json:"recipients,omitempty"`
}

// RecipientStatus is one of the lab's reported addresses (Email) and how
// it stands (State): RecipientWaiting for it to confirm (Asked is when
// Cloud emailed it the link, zero while the limits hold the email back),
// RecipientConfirmed (it gets Cloud's emails about the lab), RecipientDeclined
// (it said no, or stopped them), or RecipientNotKept (past the most
// Cloud keeps for a lab).
type RecipientStatus struct {
	Email string    `json:"email"`
	State string    `json:"state"`
	Asked time.Time `json:"asked,omitzero"`
}

// Recipient states (RecipientStatus.State).
const (
	RecipientWaiting   = "waiting"
	RecipientConfirmed = "confirmed"
	RecipientDeclined  = "declined"
	RecipientNotKept   = "not_kept"
)

// NameStatus is one of the lab's names that doesn't work because of
// Cloud (Host, in full): a name that looks like a bank's, a government
// service's or a brand's waits for approval (NameWaiting), and may be
// refused (NameRefused); any name may be suspended after an abuse report
// (NameSuspended). Reason says why, for people, when refused or
// suspended; Since is when it started.
type NameStatus struct {
	Host   string    `json:"host"`
	State  string    `json:"state"`
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since,omitzero"`
}

// Name states (NameStatus.State).
const (
	NameWaiting   = "waiting"
	NameRefused   = "refused"
	NameSuspended = "suspended"
)

// LabReport is what the lab tells Cloud after each check-in. A field left
// out (null) is unknown, and Cloud keeps what it had; an empty list
// replaces it with none.
type LabReport struct {
	// Name is what the lab calls itself now (Roostack's Settings → Lab
	// name), which Cloud shows for it in place of the name it gave when it
	// linked: one line, at most MaxLabName characters ("": unknown, Cloud
	// keeps the name it has).
	Name string `json:"name,omitempty"`
	// Emails are the addresses the lab's own problems are emailed to
	// (Roostack's SPEC.md §6.13): Cloud's emails about the lab go to them
	// too, once each has confirmed (LabInfo.Recipients). At most
	// MaxReportEmails, of which Cloud keeps the first 10.
	Emails []string `json:"emails"`
	// Backups are the lab's servers backing up to Roostack Cloud, each
	// with its last backup there. At most MaxReportServers.
	Backups []ServerBackup `json:"backups"`
	// Paused is how much of the lab's public access is paused (Roostack's
	// SPEC.md §6.16), never which services or why: Cloud doesn't learn
	// what the lab runs.
	Paused *PublicPaused `json:"paused,omitempty"`
}

// PublicPaused is how many of a lab's public services have their public
// access paused (0: none), and since when the earliest of those pauses
// has been in place.
type PublicPaused struct {
	Services int       `json:"services"`
	Since    time.Time `json:"since,omitzero"`
}

// The most a LabReport carries.
const (
	MaxReportEmails  = 20
	MaxReportServers = 100
)

// MaxLabName is the longest lab name Cloud takes, in characters (a
// LinkRequest's LabName, a LabReport's Name).
const MaxLabName = 80

// ServerBackup is a server's backups in Roostack Cloud: Server is its
// folder there (StorageRequest.Server), Name what the lab calls it, and
// Last when its last backup there finished (zero: none yet).
type ServerBackup struct {
	Server string    `json:"server"`
	Name   string    `json:"name,omitempty"`
	Last   time.Time `json:"last,omitzero"`
}

// NameAvailability says whether a roost.ac name can be had.
// RelayRequest asks Cloud to bring public visitors for the lab's names
// to it through the relay: its WireGuard key on the relay network, and
// its public names (in full, each the lab's roost.ac name or one under
// it, or a name under Domain). Asking again replaces all of it.
type RelayRequest struct {
	PublicKey string   `json:"public_key"`
	Hosts     []string `json:"hosts"`
	// Countries is who the edges let in by country (none set: anyone).
	Countries RelayCountries `json:"countries"`
	// TimeZone is the lab's (IANA, e.g. Australia/Sydney): its statistics'
	// days run midnight to midnight there. Empty is UTC.
	TimeZone string `json:"time_zone,omitempty"`
	// Domain is the lab's own domain (e.g. lab.example.com), whose names
	// in Hosts are relayed once it's proven the lab's (Relay.Domain): they
	// point at Relay.Name with a CNAME.
	Domain string `json:"domain,omitempty"`
	// TurnOn turns the relay on again after its account turned it off
	// at Cloud (Relay.TurnedOff): only when the lab's Owner asks, since
	// the lab's own updates leave it off. A Cloud from before it ignores
	// it (and never says the relay is off).
	TurnOn bool `json:"turn_on,omitempty"`
	// Resume lifts a pause the account placed at the relay's edges from
	// Cloud's website (Relay.Paused): only when the lab's Owner asks. A
	// Cloud from before it ignores it (and never pauses there).
	Resume bool `json:"resume,omitempty"`
}

// RelayDomain is how the lab's own domain stands: the TXT record that
// proves it's the lab's (ProofName, e.g. _roostack.lab.example.com, with
// ProofValue), whether Cloud has found it (Verified: its names are
// relayed), and whether it's gone missing since (relaying stops a day
// later). Problem says what Cloud saw, when it isn't there.
type RelayDomain struct {
	Domain     string    `json:"domain"`
	ProofName  string    `json:"proof_name"`
	ProofValue string    `json:"proof_value"`
	Verified   bool      `json:"verified"`
	Missing    bool      `json:"missing,omitempty"`
	CheckedAt  time.Time `json:"checked_at,omitzero"`
	Problem    string    `json:"problem,omitempty"`
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
	// AsOf is when the edges last sent their counts (the oldest of them):
	// what's happened since isn't in yet. Zero before any has.
	AsOf time.Time `json:"as_of,omitzero"`
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

// Roostack Cloud backup (Cloud's DESIGN §6(b)): each server's restic
// repository in Cloud's bucket, reached with short-lived keys the lab asks
// for. Cloud never holds a restic key: backups stay encrypted with the
// lab's own, and the lab prunes them.

// What a lab asks keys for.
const (
	// StorageBackup is a server's backups: its own folder, read and write,
	// deleting only restic's locks.
	StorageBackup = "backup"
	// StoragePrune is the controller's weekly prune: the lab's folders,
	// deleting allowed (deleted data is kept 90 days as old versions).
	StoragePrune = "prune"
	// StorageRestore is reading the lab's folders to restore it (and
	// restic's locks, which it takes even to read): RestoreAccess's keys.
	StorageRestore = "restore"
)

// StorageRequest asks for keys: for a server's backups, or the lab's
// prune. Server is the server's ID in the lab (e.g. n-3f9c2a7b1d, never
// its name, which people can change), and names its folder in Cloud's
// bucket: Cloud takes lowercase letters, digits and hyphens, at most 40,
// starting and ending with a letter or digit.
type StorageRequest struct {
	Purpose string `json:"purpose"`
	Server  string `json:"server,omitempty"`
}

// StorageKeys are S3 keys that expire (Expires): restic's repository is
// Repository (s3:<endpoint>/<bucket>/<prefix>), with AccessKeyID,
// SecretAccessKey and SessionToken (AWS_SESSION_TOKEN) as its credentials.
type StorageKeys struct {
	Repository      string    `json:"repository"`
	Endpoint        string    `json:"endpoint"`
	Region          string    `json:"region"`
	Bucket          string    `json:"bucket"`
	Prefix          string    `json:"prefix"`
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	Expires         time.Time `json:"expires"`
}

// StorageUsage is what the lab stores in Roostack Cloud, as last measured
// (daily), and its quota (zero: none). Bytes are decimal.
type StorageUsage struct {
	// QuotaBytes is the account's, which its labs share, and UsedBytes
	// this lab's use of it.
	QuotaBytes int64           `json:"quota_bytes"`
	UsedBytes  int64           `json:"used_bytes"`
	MeasuredAt time.Time       `json:"measured_at,omitzero"`
	Servers    []ServerStorage `json:"servers"`
	// AccountUsedBytes is what all the account's linked labs store
	// together, this one's included: what Cloud holds against QuotaBytes,
	// and warns the account about at 80% and 100%. Zero from a Cloud
	// before it (2026-10-19): take UsedBytes.
	AccountUsedBytes int64 `json:"account_used_bytes,omitempty"`
}

// ServerStorage is one server's share of StorageUsage.
type ServerStorage struct {
	Server string `json:"server"`
	Bytes  int64  `json:"bytes"`
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
	// Name is the lab's relay name (r-….relay.roost.ac): its own domain's
	// names point at it with a CNAME, and Cloud keeps it pointing at the
	// edges. Domain is how that domain stands, if there is one.
	Name   string       `json:"name,omitempty"`
	Domain *RelayDomain `json:"domain,omitempty"`
	// TurnedOff is when the lab's account turned the relay off at Cloud
	// (zero: it's on, or a Cloud from before it doesn't say): visitors no
	// longer come through it, and its names have the lab's own records
	// again, until the lab asks with RelayRequest.TurnOn.
	TurnedOff time.Time `json:"turned_off,omitzero"`
	// Suspended is why Cloud suspended the relay after an abuse report
	// ("": it isn't): visitors don't come through it until it's lifted.
	Suspended string `json:"suspended,omitempty"`
	// Paused is when the lab's account paused public access at the
	// relay's edges from Cloud's website (zero: it isn't, or a Cloud from
	// before it doesn't say; Roostack's SPEC.md §6.16): the edges close
	// connections for the lab's names, which still point at them, even
	// while the lab is down. It lasts until the account lifts it, or the
	// lab asks with RelayRequest.Resume.
	Paused time.Time `json:"paused,omitzero"`
	// Names are the lab's names that don't work because of Cloud, as in
	// LabInfo.Names.
	Names []NameStatus `json:"names,omitempty"`
	// Registry is as in News.
	Registry int64 `json:"registry,omitempty"`
}

// News is what a linked lab hears from Cloud every few minutes (Roostack's
// SPEC.md §6.16). Registry is the serial of the newest signed index of the
// service registry Cloud has seen (0: unknown): when it's newer than the
// lab's own, the lab fetches the registry at once, rather than at its
// next hourly check. Cloud only says when to look: the lab fetches the
// index from the registry and checks its signature as always.
type News struct {
	Registry int64 `json:"registry,omitempty"`
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

// NameRequest claims a roost.ac name for the lab. KeepEarlier keeps the
// lab's name until now working (its records and challenge login) beside
// the new one, until the lab releases it (PathLabNameRelease) or
// KeptFor passes: the lab can go back to it if using the new one fails,
// and its old addresses keep working while people update them (SPEC.md
// §6.6). Without it (an older lab), the earlier name is released with
// the claim. Only one earlier name is kept: claiming again ends the one
// before.
type NameRequest struct {
	Name        string `json:"name"`
	KeepEarlier bool   `json:"keep_earlier,omitempty"`
}

// KeptFor is how long Cloud keeps a lab's earlier name working after a
// claim with KeepEarlier, if the lab doesn't release it first: a day
// more than the lab keeps its old addresses (30 days).
const KeptFor = 31 * 24 * time.Hour

// KeptName is the earlier name a claim kept, and until when.
type KeptName struct {
	Name  string    `json:"name"`
	Until time.Time `json:"until"`
}

// NameRelease is a name the lab is done with (its full name): the
// earlier one it kept, which then goes (held for the account, as an
// unlinked lab's), or the one it just claimed, which goes the same way
// while the kept earlier one is the lab's again, as it was.
type NameRelease struct {
	Name string `json:"name"`
}

// NameReleased is the lab's name in use after a release ("": none).
type NameReleased struct {
	InUse string `json:"in_use"`
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
	// Kept is the lab's earlier name, kept working (NameRequest.
	// KeepEarlier); nil if there was none, or it was released (a Cloud
	// from before it).
	Kept *KeptName `json:"kept,omitempty"`
	// Status is set when the name doesn't work yet: waiting for approval,
	// or refused for the account before (nil: it works, or a Cloud from
	// before it doesn't say).
	Status *NameStatus `json:"status,omitempty"`
}

// The recovery identity (Cloud's DESIGN §3b, "Account recovery"): an
// Ed25519 key the lab derives from its recovery key, so whoever holds the
// recovery key can prove it to Cloud without revealing it. The lab
// registers the public half, with Signature: RestoreMessage(ProveRegister,
// its lab ID) signed with the key, so a lab can only register a key it
// holds (Cloud refuses it otherwise). Cloud finds the lab by it.
type RecoveryIdentity struct {
	PublicKey []byte `json:"public_key"`
	Signature []byte `json:"signature"`
}

// RestoreChallengeRequest asks for a challenge to sign.
type RestoreChallengeRequest struct {
	PublicKey []byte `json:"public_key"`
}

// RestoreChallenge is a value to sign, once, before it expires.
type RestoreChallenge struct {
	Challenge string    `json:"challenge"`
	Expires   time.Time `json:"expires"`
}

// What a signature is for (part of what's signed, so a signature for one
// can't be used for another).
const (
	ProveAccess = "access"
	ProveRelink = "relink"
	// ProveRegister signs the lab's own ID, not a challenge: registering
	// the recovery identity (RecoveryIdentity).
	ProveRegister = "register"
)

// RestoreMessage is what the lab signs: the challenge (or, to register,
// its lab ID), for one purpose.
func RestoreMessage(purpose, challenge string) []byte {
	return []byte("roostack-recovery-identity-v1\n" + purpose + "\n" + challenge)
}

// RestoreProof is a challenge signed with the recovery identity's key.
type RestoreProof struct {
	PublicKey []byte `json:"public_key"`
	Challenge string `json:"challenge"`
	Signature []byte `json:"signature"`
}

// RestoreAccess opens the backups in Roostack Cloud of every lab with the
// recovery key, for a restore: the linked one first, then those unlinked,
// the latest unlinked first. A lab unlinked and linked again keeps its
// recovery key under a new lab ID, and each has its own backups, so the
// person restoring chooses.
type RestoreAccess struct {
	Labs []RestoreLab `json:"labs"`
}

// RestoreLab is one lab's backups: keys that read its folders, and the
// repository of each server in them (none, once they're no longer kept).
type RestoreLab struct {
	LabID   string `json:"lab_id"`
	LabName string `json:"lab_name"`
	// LinkedAt is when it was linked, and UnlinkedAt when it was unlinked
	// (zero while it's linked).
	LinkedAt     time.Time   `json:"linked_at"`
	UnlinkedAt   time.Time   `json:"unlinked_at,omitzero"`
	Keys         StorageKeys `json:"keys"`
	Repositories []string    `json:"repositories"`
}

// RelinkRequest asks for a new certificate for a lab whose own is no
// longer accepted (it expired while the lab was off, say), for a new key.
// Cloud refuses it for a lab that was unlinked.
type RelinkRequest struct {
	RestoreProof
	CSR []byte `json:"csr"`
}

// Relinked is the lab's new certificate (PEM).
type Relinked struct {
	LabInfo
	Certificate string `json:"certificate"`
}
