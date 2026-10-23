package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"time"
	"unicode/utf8"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/fsutil"
)

// The request log, requests.log in the lab's Cloud folder: every request
// the lab makes to Roostack Cloud, with what it sent but never a secret,
// so the Owner can check what leaves the lab rather than take it on trust
// (Settings → Roostack Cloud, the help's "Your data and your keys";
// SPEC.md §6.6). One JSON object a line, the latest keepRequests.

// Request is one request the lab made to Cloud.
type Request struct {
	At     time.Time
	Method string
	// Path is the request's path at Cloud's API, or the whole address for
	// another of Cloud's servers (the challenge server).
	Path string
	// Sent is what the request sent (its JSON body), with secrets left out
	// (loggedBody); empty for a request without a body.
	Sent string `json:",omitempty"`
	// Status is the HTTP status; 0 if Cloud wasn't reached (Error says why).
	Status int
	Error  string
	// Count is how many times in a row it was made with the same answer
	// (asking about a link every few seconds is one line), up to At.
	Count int `json:",omitempty"`
}

// recordedFields are the fields of the lab's requests to Cloud
// (cloudapi's request types) whose values the log shows. Any other field
// shows as notRecorded: secrets, and any field added later, until someone
// decides it's safe to show. TestRecordedFields fails while a request
// type has a field missing here, so each is decided.
var recordedFields = map[string]bool{
	// LinkRequest: what the lab tells Cloud when it links. csr is the
	// certificate request (its public key; the private key stays).
	"lab_name": true, "name": true, "version": true, "csr": true, "return_url": true,
	// NameRequest's keep_earlier (NameRelease's name is above).
	"keep_earlier": true,
	// LinkSecret: the secret that finishes linking, which only the lab
	// and Cloud know.
	"secret": false,
	// LabReport and its ServerBackup.
	"emails": true, "backups": true, "server": true, "last": true,
	// LabReport's PublicPaused: how many services are paused and since
	// when, never which (SPEC.md §6.16).
	"paused": true, "services": true, "since": true,
	// RelayRequest (public_key is the lab's WireGuard public key),
	// RelayCountries, RelayBans.
	"public_key": true, "hosts": true, "countries": true, "allow": true, "block": true,
	"open_until": true, "time_zone": true, "domain": true, "turn_on": true, "resume": true, "pause": true, "bans": true,
	// StorageRequest.
	"purpose": true,
	// RecoveryIdentity, RestoreChallengeRequest, RestoreProof and
	// RelinkRequest: a signature proves the lab holds the recovery key,
	// and is accepted once, so it's left out like a password.
	"challenge": true, "signature": false,
	// ACMEUpdate: a test value written through Cloud's challenge server
	// when the lab gets a name (checkClaim). Its login goes in headers,
	// which aren't recorded.
	"subdomain": true, "txt": true,
	// RecordValue: a roost.ac name's record, set through Cloud's records
	// API (dns/cloud.go): the lab's address at home or its public one,
	// which public DNS shows anyway.
	"value": true,
}

// notRecorded stands for a value the log leaves out.
const notRecorded = "(not recorded)"

// Long values (a certificate request, a list of bans) are shortened: the
// log is for seeing what kind of thing was sent, and stays small.
const (
	longValue = 64
	longSent  = 2000
)

// loggedBody is what the log shows of a request's JSON body: its fields,
// with recordedFields' values and notRecorded for any other, long values
// shortened. Anything that isn't a JSON object shows only its size.
func loggedBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return fmt.Sprintf("(%d bytes)", len(body))
	}
	if _, ok := v.(map[string]any); !ok {
		return fmt.Sprintf("(%d bytes)", len(body))
	}
	out, _ := json.Marshal(redact(v))
	if len(out) > longSent {
		cut := longSent
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		return string(out[:cut]) + fmt.Sprintf("… (%d bytes in all)", len(out))
	}
	return string(out)
}

// redact walks a decoded JSON value, keeping recordedFields.
func redact(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			if recordedFields[k] {
				out[k] = redact(val)
			} else {
				out[k] = notRecorded
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = redact(val)
		}
		return out
	case string:
		if n := utf8.RuneCountInString(v); n > longValue {
			return string([]rune(v)[:24]) + fmt.Sprintf("… (%d characters)", n)
		}
		return v
	}
	return v
}

// record appends a request to requests.log, keeping the latest few.
func (s *Service) record(method, path string, body []byte, status int, err error) {
	r := Request{At: s.now().UTC(), Method: method, Path: path, Sent: loggedBody(body), Status: status}
	if err != nil {
		r.Error = err.Error()
	}
	line, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	old, _ := os.ReadFile(s.file("requests.log"))
	lines := bytes.Split(bytes.TrimSpace(old), []byte("\n"))
	if len(old) == 0 {
		lines = nil
	}
	// The same request with the same answer as the last one adds to it.
	var last Request
	if n := len(lines); n > 0 && json.Unmarshal(lines[n-1], &last) == nil &&
		last.Method == r.Method && last.Path == r.Path && last.Sent == r.Sent && last.Status == r.Status && last.Error == r.Error {
		r.Count = max(last.Count, 1) + 1
		line, _ = json.Marshal(r)
		lines = lines[:n-1]
	}
	lines = append(lines, line)
	if len(lines) > keepRequests {
		lines = lines[len(lines)-keepRequests:]
	}
	if err := fsutil.WritePrivate(s.file("requests.log"), append(bytes.Join(lines, []byte("\n")), '\n')); err != nil {
		s.log.Warn("Roostack Cloud: recording a request", "err", err)
	}
}

// Requests are the lab's recent requests to Cloud, newest first.
func (s *Service) Requests() []Request {
	s.mu.Lock()
	data, _ := os.ReadFile(s.file("requests.log"))
	s.mu.Unlock()
	var out []Request
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var r Request
		if json.Unmarshal(line, &r) == nil && !r.At.IsZero() {
			out = append([]Request{r}, out...)
		}
	}
	return out
}

// KeepRequests is for a lab restored onto this install: it reads the
// request log now, before the restore puts back the lab's own (its Cloud
// folder), and keepAgain adds these requests to it afterwards. Without
// it, what the new install asked Cloud while restoring (RestoreAccess)
// would leave no trace (SPEC.md §6.6).
func (s *Service) KeepRequests() (keepAgain func() error) {
	s.mu.Lock()
	ours, _ := os.ReadFile(s.file("requests.log"))
	s.mu.Unlock()
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		theirs, _ := os.ReadFile(s.file("requests.log"))
		var all []Request
		for _, data := range [][]byte{theirs, ours} {
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
				var r Request
				if json.Unmarshal(line, &r) == nil && !r.At.IsZero() {
					all = append(all, r)
				}
			}
		}
		if len(all) == 0 {
			return nil
		}
		slices.SortStableFunc(all, func(a, b Request) int { return a.At.Compare(b.At) })
		all = slices.CompactFunc(all, func(a, b Request) bool { return a == b })
		if len(all) > keepRequests {
			all = all[len(all)-keepRequests:]
		}
		var out []byte
		for _, r := range all {
			line, _ := json.Marshal(r)
			out = append(append(out, line...), '\n')
		}
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return err
		}
		return fsutil.WritePrivate(s.file("requests.log"), out)
	}
}

// RecordsTransport has the request log show the lab's changes to a
// roost.ac name's records through Cloud's records API (the DNS provider
// in dns/cloud.go, through the records manager's HTTP client): requests
// carrying Cloud's login header (cloudapi.HeaderUser, which only they
// and the challenge server use) that change something. Reads aren't
// recorded: they send only the record's name, and the records manager
// reads every record every few minutes while anything is public, which
// would push everything else out of the log. The login itself is in
// headers, which aren't recorded. Other requests pass by unrecorded.
func (s *Service) RecordsTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get(cloudapi.HeaderUser) == "" || req.Method == http.MethodGet || req.Method == http.MethodHead {
			return next.RoundTrip(req)
		}
		var body []byte
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				body, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
		resp, err := next.RoundTrip(req)
		status := 0
		if err == nil {
			status = resp.StatusCode
		}
		s.record(req.Method, req.URL.String(), body, status, err)
		return resp, err
	})
}

// roundTripper is a function as an http.RoundTripper.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
