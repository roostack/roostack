package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
	"unicode/utf8"
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
	// LinkSecret: the secret that finishes linking, which only the lab
	// and Cloud know.
	"secret": false,
	// LabReport and its ServerBackup.
	"emails": true, "backups": true, "server": true, "last": true,
	// RelayRequest (public_key is the lab's WireGuard public key),
	// RelayCountries, RelayBans.
	"public_key": true, "hosts": true, "countries": true, "allow": true, "block": true,
	"open_until": true, "time_zone": true, "domain": true, "turn_on": true, "bans": true,
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
	if err := writeFile(s.file("requests.log"), append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
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
