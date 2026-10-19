package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
)

// Every field of every request the lab sends to Cloud is decided: shown
// in the request log, or left out. A new field fails here until it's in
// recordedFields.
func TestRecordedFields(t *testing.T) {
	sent := []any{
		cloudapi.LinkRequest{}, cloudapi.LinkSecret{}, cloudapi.LabReport{}, cloudapi.RelayRequest{},
		cloudapi.RelayBans{}, cloudapi.StorageRequest{}, cloudapi.RenewRequest{}, cloudapi.NameRequest{},
		cloudapi.RecoveryIdentity{}, cloudapi.RestoreChallengeRequest{}, cloudapi.RestoreProof{},
		cloudapi.RelinkRequest{}, cloudapi.ACMEUpdate{}, cloudapi.RecordValue{}, cloudapi.NameRelease{},
	}
	var walk func(reflect.Type)
	walk = func(ty reflect.Type) {
		for ty.Kind() == reflect.Slice || ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Map {
			if ty.Kind() == reflect.Slice && ty.Elem().Kind() == reflect.Uint8 {
				return // []byte is a value (base64)
			}
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || ty.PkgPath() != reflect.TypeFor[cloudapi.LinkRequest]().PkgPath() {
			return
		}
		for f := range ty.Fields() {
			if f.Anonymous {
				walk(f.Type)
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if _, ok := recordedFields[name]; !ok {
				t.Errorf("%s.%s (%q): decide whether the request log may show it (recordedFields)", ty.Name(), f.Name, name)
			}
			walk(f.Type)
		}
	}
	for _, v := range sent {
		walk(reflect.TypeOf(v))
	}
}

func TestLoggedBody(t *testing.T) {
	sig := bytes.Repeat([]byte{0xAB}, 64)
	csr := bytes.Repeat([]byte("c"), 600)
	body, _ := json.Marshal(cloudapi.RelinkRequest{
		RestoreProof: cloudapi.RestoreProof{PublicKey: []byte("pk"), Challenge: "ch-1", Signature: sig},
		CSR:          csr,
	})
	got := loggedBody(body)
	if strings.Contains(got, base64.StdEncoding.EncodeToString(sig)[:20]) {
		t.Errorf("the signature is in the log: %s", got)
	}
	for _, want := range []string{`"signature":"(not recorded)"`, `"challenge":"ch-1"`, `"public_key":"cGs="`, `… (800 characters)"`} {
		if !strings.Contains(got, want) {
			t.Errorf("logged %s, missing %s", got, want)
		}
	}
	if len(got) > 300 {
		t.Errorf("a long value isn't shortened: %d bytes", len(got))
	}
	// A field nobody decided about is left out.
	if got := loggedBody([]byte(`{"token":"s3cret","name":"smiths"}`)); got != `{"name":"smiths","token":"(not recorded)"}` {
		t.Errorf("logged %s", got)
	}
	if got := loggedBody(nil); got != "" {
		t.Errorf("no body logged as %q", got)
	}
	// A long list is cut, not kept whole.
	bans := make([]string, 500)
	for i := range bans {
		bans[i] = "203.0.113.1"
	}
	body, _ = json.Marshal(cloudapi.RelayBans{Bans: bans})
	if got := loggedBody(body); len(got) > longSent+40 || !strings.HasSuffix(got, " bytes in all)") {
		t.Errorf("logged %d bytes: …%s", len(got), got[max(0, len(got)-40):])
	}
}

// Linking logs what the lab told Cloud, but not the link's secret.
func TestRequestLogLeavesOutSecrets(t *testing.T) {
	f, s, state := fakeCloud(t)
	link(t, f, s, "smiths")
	data, err := os.ReadFile(filepath.Join(Dir(state), "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	var linked, polled bool
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var r Request
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		var sent map[string]any
		if r.Sent != "" {
			if err := json.Unmarshal([]byte(r.Sent), &sent); err != nil {
				t.Fatalf("Sent %q: %v", r.Sent, err)
			}
		}
		switch r.Path {
		case cloudapi.PathLinks:
			linked = sent["lab_name"] == "Smith family" && sent["name"] == "smiths" && sent["version"] == "test"
		case cloudapi.PathLinkStatus:
			polled = sent["secret"] == notRecorded
		}
		for k, v := range sent {
			if !recordedFields[k] && v != notRecorded {
				t.Errorf("%s %s logged %s: %v", r.Method, r.Path, k, v)
			}
		}
	}
	if !linked || !polled {
		t.Errorf("linking isn't logged as it should be:\n%s", data)
	}
}

// TestRecordsInRequestLog: a roost.ac name's records, set through
// Cloud's records API by the DNS provider, are in the request log (what
// was set, never the login); reading them isn't, nor requests to
// anything else.
func TestRecordsInRequestLog(t *testing.T) {
	ctx := context.Background()
	f, s, state := fakeCloud(t)
	var claim cloudapi.NameClaim
	s.UseName = func(_ context.Context, c cloudapi.NameClaim) error { claim = c; return nil }
	link(t, f, s, "")
	if err := s.ClaimName(ctx, "smiths"); err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: s.RecordsTransport(s.client.anon.Transport)}
	p := dns.Get(config.ProviderCloud)
	if err := p.SetRecord(ctx, hc, claim.Credentials, claim.Name, "", "A", "192.168.1.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetRecord(ctx, hc, claim.Credentials, claim.Name, "", "A"); err != nil {
		t.Fatal(err)
	}
	// Something else through the same client: not Cloud's.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	if resp, err := hc.Get(other.URL); err == nil {
		resp.Body.Close()
	}
	got := s.Requests()
	if len(got) == 0 || got[0].Method != http.MethodPut || !strings.HasSuffix(got[0].Path, "/v1/records/@/A") ||
		got[0].Sent != `{"value":"192.168.1.10"}` || got[0].Status != http.StatusOK {
		t.Errorf("the records' requests: %+v", got[:min(2, len(got))])
	}
	data, _ := os.ReadFile(filepath.Join(Dir(state), "requests.log"))
	if bytes.Contains(data, []byte(claim.Credentials[cloudapi.CredPassword])) || bytes.Contains(data, []byte(other.URL)) {
		t.Errorf("the log has the login, or another server's request:\n%s", data)
	}
}

// TestKeepRequests: a lab restored onto a new install keeps, in its own
// request log, what the new install asked Cloud while restoring.
func TestKeepRequests(t *testing.T) {
	_, s, _ := fakeCloud(t)
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	s.record(http.MethodPost, cloudapi.PathRestoreAccess, nil, http.StatusOK, nil)
	keepAgain := s.KeepRequests()
	// The restore puts back the lab's own log, from before.
	old := Request{At: at.Add(-time.Hour), Method: http.MethodGet, Path: cloudapi.PathLab, Status: http.StatusOK}
	line, _ := json.Marshal(old)
	if err := os.WriteFile(s.file("requests.log"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keepAgain(); err != nil {
		t.Fatal(err)
	}
	got := s.Requests()
	if len(got) != 2 || got[0].Path != cloudapi.PathRestoreAccess || got[1].Path != cloudapi.PathLab {
		t.Errorf("after the restore: %+v", got)
	}
	// Twice changes nothing.
	keepAgain()
	if n := len(s.Requests()); n != 2 {
		t.Errorf("%d requests after keeping them again", n)
	}
}
