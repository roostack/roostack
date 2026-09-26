package dns

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPIs imitates each provider's API for the zone example.com, records
// every request, and keeps the TXT records that currently exist.
type fakeAPIs struct {
	mu      sync.Mutex
	calls   []string
	records map[string]bool
}

func (f *fakeAPIs) log(r *http.Request, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" auth="+auth)
}

func (f *fakeAPIs) set(name string, exists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if exists {
		f.records[name] = true
	} else {
		delete(f.records, name)
	}
}

func startFakeAPIs(t *testing.T) *fakeAPIs {
	t.Helper()
	f := &fakeAPIs{records: map[string]bool{}}
	mux := http.NewServeMux()
	body := func(r *http.Request) map[string]any {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		return m
	}

	// Cloudflare
	mux.HandleFunc("GET /cf/zones", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, r.Header.Get("Authorization"))
		if r.URL.Query().Get("name") == "example.com" {
			w.Write([]byte(`{"success":true,"result":[{"id":"z1"}]}`))
		} else {
			w.Write([]byte(`{"success":true,"result":[]}`))
		}
	})
	mux.HandleFunc("POST /cf/zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, r.Header.Get("Authorization"))
		f.set(body(r)["name"].(string), true)
		w.Write([]byte(`{"success":true,"result":{"id":"rec1"}}`))
	})
	mux.HandleFunc("DELETE /cf/zones/z1/dns_records/rec1", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, r.Header.Get("Authorization"))
		f.set("_mylab-check.lab.example.com", false)
		w.Write([]byte(`{"success":true}`))
	})

	// Route 53
	mux.HandleFunc("GET /r53/2013-04-01/hostedzonesbyname", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, strings.SplitN(r.Header.Get("Authorization"), " ", 2)[0])
		name := r.URL.Query().Get("dnsname")
		if name != "example.com" {
			name = "zzz.org" // Route 53 returns the next zone alphabetically
		}
		w.Write([]byte(`<ListHostedZonesByNameResponse><HostedZones><HostedZone><Id>/hostedzone/Z9</Id><Name>` + name + `.</Name></HostedZone></HostedZones></ListHostedZonesByNameResponse>`))
	})
	mux.HandleFunc("POST /r53/2013-04-01/hostedzone/Z9/rrset", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, strings.SplitN(r.Header.Get("Authorization"), " ", 2)[0])
		b, _ := io.ReadAll(r.Body)
		f.set("_mylab-check.lab.example.com", strings.Contains(string(b), "<Action>CREATE</Action>"))
		w.Write([]byte(`<ChangeResourceRecordSetsResponse/>`))
	})

	// Porkbun (errors in the body)
	mux.HandleFunc("POST /pb/dns/create/{zone}", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		f.log(r, b["apikey"].(string))
		if r.PathValue("zone") != "example.com" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":"ERROR","message":"Invalid domain."}`))
			return
		}
		f.set(b["name"].(string)+".example.com", true)
		w.Write([]byte(`{"status":"SUCCESS","id":123}`))
	})
	mux.HandleFunc("POST /pb/dns/delete/example.com/123", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, body(r)["apikey"].(string))
		f.set("_mylab-check.lab.example.com", false)
		w.Write([]byte(`{"status":"SUCCESS"}`))
	})

	// Gandi, GoDaddy: PUT then DELETE on the record URL
	for _, prefix := range []string{"/gandi/domains/{zone}/records/{name}/TXT", "/gd/v1/domains/{zone}/records/TXT/{name}"} {
		mux.HandleFunc("PUT "+prefix, func(w http.ResponseWriter, r *http.Request) {
			f.log(r, r.Header.Get("Authorization"))
			if r.PathValue("zone") != "example.com" {
				http.Error(w, `{"message":"domain not found"}`, http.StatusNotFound)
				return
			}
			f.set(r.PathValue("name")+".example.com", true)
			w.WriteHeader(http.StatusCreated)
		})
		mux.HandleFunc("DELETE "+prefix, func(w http.ResponseWriter, r *http.Request) {
			f.log(r, r.Header.Get("Authorization"))
			f.set(r.PathValue("name")+".example.com", false)
			w.WriteHeader(http.StatusNoContent)
		})
	}

	// Spaceship
	mux.HandleFunc("/ss/v1/dns/records/{zone}", func(w http.ResponseWriter, r *http.Request) {
		f.log(r, r.Header.Get("X-API-Key")+"/"+r.Header.Get("X-API-Secret"))
		if r.PathValue("zone") != "example.com" {
			http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
			return
		}
		f.set("_mylab-check.lab.example.com", r.Method == "PUT")
		w.WriteHeader(http.StatusNoContent)
	})

	// Namecheap (read-only) and the public IP service
	mux.HandleFunc("/nc", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		q := r.Form
		f.log(r, q.Get("ApiKey")+"@"+q.Get("ClientIp"))
		if q.Get("SLD") == "example" && q.Get("TLD") == "com" && q.Get("ApiKey") == "good" {
			w.Write([]byte(`<ApiResponse Status="OK"><CommandResponse/></ApiResponse>`))
			return
		}
		w.Write([]byte(`<ApiResponse Status="ERROR"><Errors><Error Number="1011150">Invalid request IP</Error></Errors></ApiResponse>`))
	})
	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("203.0.113.7\n")) })

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for ptr, path := range map[*string]string{
		&cloudflareAPI: "/cf", &route53API: "/r53", &porkbunAPI: "/pb", &gandiAPI: "/gandi",
		&godaddyAPI: "/gd", &spaceshipAPI: "/ss", &namecheapAPI: "/nc", &publicIPURL: "/ip",
	} {
		old := *ptr
		*ptr = srv.URL + path
		t.Cleanup(func() { *ptr = old })
	}
	return f
}

func TestProbes(t *testing.T) {
	cases := map[string]struct {
		values   map[string]string
		wantAuth string // must appear in the logged requests
	}{
		"cloudflare": {map[string]string{"api_token": "tok"}, "auth=Bearer tok"},
		"route53":    {map[string]string{"access_key_id": "AKID", "secret_access_key": "s"}, "auth=AWS4-HMAC-SHA256"},
		"porkbun":    {map[string]string{"api_key": "pk", "api_secret_key": "sk"}, "auth=pk"},
		"gandi":      {map[string]string{"bearer_token": "pat"}, "auth=Bearer pat"},
		"godaddy":    {map[string]string{"api_key": "k", "api_secret": "s"}, "auth=sso-key k:s"},
		"spaceship":  {map[string]string{"api_key": "k", "api_secret": "s"}, "auth=k/s"},
		"namecheap":  {map[string]string{"user": "me", "api_key": "good"}, "auth=good@203.0.113.7"},
	}
	for id, c := range cases {
		t.Run(id, func(t *testing.T) {
			f := startFakeAPIs(t)
			zone, err := check(context.Background(), http.DefaultClient, Get(id).probe, c.values, "lab.example.com")
			if err != nil || zone != "example.com" {
				t.Fatalf("zone %q err %v\ncalls: %v", zone, err, f.calls)
			}
			if !strings.Contains(strings.Join(f.calls, "\n"), c.wantAuth) {
				t.Errorf("no request with %q:\n%s", c.wantAuth, strings.Join(f.calls, "\n"))
			}
			if len(f.records) != 0 {
				t.Errorf("test records left behind: %v", f.records)
			}
		})
	}
}

func TestNamecheapErrorShowsIP(t *testing.T) {
	startFakeAPIs(t)
	_, err := check(context.Background(), http.DefaultClient, Get("namecheap").probe,
		map[string]string{"user": "me", "api_key": "bad", "client_ip": "198.51.100.1"}, "example.com")
	if err == nil || !strings.Contains(err.Error(), "Invalid request IP (error 1011150) (this lab's IP is 198.51.100.1)") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), "ApiKey") || strings.Contains(err.Error(), "bad") {
		t.Error("errors must not contain the API key")
	}
}
