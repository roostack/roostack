package dns

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// recordStore is a fake DNS provider backend shared by every fake API:
// (zone, name, type) -> values. Namecheap also keeps other records and the
// domain's email setting, to prove its full rewrite preserves them.
type recordStore struct {
	mu        sync.Mutex
	recs      map[string][]string
	ncHosts   []ncHost
	ncEmail   string
	cfComment string
}

func key(zone, name, typ string) string { return zone + "|" + name + "|" + typ }

func (s *recordStore) get(zone, name, typ string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recs[key(zone, name, typ)]
}

func (s *recordStore) set(zone, name, typ string, vals []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key(zone, name, typ)] = vals
}

func startRecordAPIs(t *testing.T) *recordStore {
	t.Helper()
	st := &recordStore{recs: map[string][]string{},
		ncEmail: "FWD", ncHosts: []ncHost{{Name: "@", Type: "MX", Address: "mail.example.net.", MXPref: "10", TTL: "1800"},
			{Name: "www", Type: "CNAME", Address: "example.com.", TTL: "1800"}}}
	mux := http.NewServeMux()
	readJSON := func(r *http.Request, v any) { b, _ := io.ReadAll(r.Body); json.Unmarshal(b, v) }
	rel := func(fq string) string { return strings.TrimSuffix(strings.TrimSuffix(fq, "."), ".example.com") }

	// Cloudflare
	mux.HandleFunc("GET /cf/zones", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":[{"id":"z1"}]}`)
	})
	// Record IDs are the name and type, so a DELETE says which.
	mux.HandleFunc("GET /cf/zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		name, typ := rel(r.URL.Query().Get("name")), r.URL.Query().Get("type")
		var res []map[string]string
		for _, v := range st.get("example.com", name, typ) {
			res = append(res, map[string]string{"id": name + "~" + typ, "content": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"result": res})
	})
	cfWrite := func(w http.ResponseWriter, r *http.Request) {
		var rec map[string]any
		readJSON(r, &rec)
		st.set("example.com", rel(rec["name"].(string)), rec["type"].(string), []string{rec["content"].(string)})
		st.cfComment, _ = rec["comment"].(string)
		fmt.Fprint(w, `{"result":{"id":"r1"}}`)
	}
	mux.HandleFunc("POST /cf/zones/z1/dns_records", cfWrite)
	mux.HandleFunc("PUT /cf/zones/z1/dns_records/{id}", cfWrite)
	mux.HandleFunc("DELETE /cf/zones/z1/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		name, typ, _ := strings.Cut(r.PathValue("id"), "~")
		st.set("example.com", name, typ, nil)
		fmt.Fprint(w, `{"result":{}}`)
	})

	// Route 53
	mux.HandleFunc("GET /r53/2013-04-01/hostedzonesbyname", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<R><HostedZones><HostedZone><Id>/hostedzone/Z9</Id><Name>example.com.</Name></HostedZone></HostedZones></R>`)
	})
	mux.HandleFunc("GET /r53/2013-04-01/hostedzone/Z9/rrset", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		vals := st.get("example.com", rel(name), r.URL.Query().Get("type"))
		fmt.Fprint(w, `<R><ResourceRecordSets>`)
		if len(vals) > 0 {
			var esc strings.Builder
			xml.EscapeText(&esc, []byte(vals[0]))
			fmt.Fprintf(w, `<ResourceRecordSet><Name>%s</Name><Type>%s</Type><TTL>300</TTL><ResourceRecords><ResourceRecord><Value>%s</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>`,
				strings.Replace(name, "*", `\052`, 1), r.URL.Query().Get("type"), esc.String())
		}
		fmt.Fprint(w, `</ResourceRecordSets></R>`)
	})
	mux.HandleFunc("POST /r53/2013-04-01/hostedzone/Z9/rrset", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := regexp.MustCompile(`<Action>(UPSERT|DELETE)</Action>.*<Name>(.*?)</Name><Type>(.*?)</Type><TTL>(.*?)</TTL>.*<Value>(.*?)</Value>`).FindStringSubmatch(string(b))
		if m == nil {
			http.Error(w, "bad request", 400)
			return
		}
		var val struct {
			V string `xml:",chardata"`
		}
		xml.Unmarshal([]byte("<v>"+m[5]+"</v>"), &val)
		name, typ := rel(m[2]), m[3]
		if m[1] == "DELETE" {
			// Route 53 refuses a delete that doesn't match the set exactly.
			if cur := st.get("example.com", name, typ); len(cur) != 1 || cur[0] != val.V || m[4] != "300" {
				http.Error(w, "InvalidChangeBatch", 400)
				return
			}
			st.set("example.com", name, typ, nil)
		} else {
			st.set("example.com", name, typ, []string{val.V})
		}
		fmt.Fprint(w, `<ok/>`)
	})

	// Porkbun
	mux.HandleFunc("POST /pb/dns/retrieveByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var recs []map[string]string
		for _, v := range st.get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
			recs = append(recs, map[string]string{"content": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "SUCCESS", "records": recs})
	})
	mux.HandleFunc("POST /pb/dns/create/{zone}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		readJSON(r, &b)
		if len(st.get(r.PathValue("zone"), b["name"].(string), b["type"].(string))) > 0 {
			fmt.Fprint(w, `{"status":"ERROR","message":"duplicate"}`)
			return
		}
		st.set(r.PathValue("zone"), b["name"].(string), b["type"].(string), []string{b["content"].(string)})
		fmt.Fprint(w, `{"status":"SUCCESS","id":1}`)
	})
	mux.HandleFunc("POST /pb/dns/deleteByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
		fmt.Fprint(w, `{"status":"SUCCESS"}`)
	})
	mux.HandleFunc("POST /pb/dns/editByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		readJSON(r, &b)
		st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), []string{b["content"].(string)})
		fmt.Fprint(w, `{"status":"SUCCESS"}`)
	})

	// Gandi and GoDaddy: GET/PUT on the record set
	mux.HandleFunc("/gandi/domains/{zone}/records/{name}/{typ}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var b struct {
				Values []string `json:"rrset_values"`
			}
			readJSON(r, &b)
			st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), b.Values)
			w.WriteHeader(201)
			return
		}
		if r.Method == "DELETE" {
			if len(st.get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))) == 0 {
				http.Error(w, `{"message":"not found"}`, 404)
				return
			}
			st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
			w.WriteHeader(204)
			return
		}
		vals := st.get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))
		if len(vals) == 0 {
			http.Error(w, `{"message":"not found"}`, 404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"rrset_values": vals})
	})
	mux.HandleFunc("/gd/v1/domains/{zone}/records/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var b []struct{ Data string }
			readJSON(r, &b)
			st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), []string{b[0].Data})
			return
		}
		if r.Method == "DELETE" {
			if len(st.get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))) == 0 {
				http.Error(w, `{"code":"NOT_FOUND"}`, 404)
				return
			}
			st.set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
			w.WriteHeader(204)
			return
		}
		var res []map[string]string
		for _, v := range st.get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
			res = append(res, map[string]string{"data": v})
		}
		if res == nil {
			res = []map[string]string{}
		}
		json.NewEncoder(w).Encode(res)
	})

	// Spaceship
	mux.HandleFunc("/ss/v1/dns/records/{zone}", func(w http.ResponseWriter, r *http.Request) {
		type item struct{ Type, Name, Address, Cname, Value string }
		field := map[string]string{"A": "address", "CNAME": "cname", "TXT": "value"}
		if r.Method == "PUT" {
			var b struct {
				Force bool
				Items []item
			}
			readJSON(r, &b)
			it := b.Items[0]
			if !b.Force && len(st.get(r.PathValue("zone"), it.Name, it.Type)) > 0 {
				http.Error(w, "conflict", 409)
				return
			}
			st.set(r.PathValue("zone"), it.Name, it.Type, []string{field[it.Type] + "=" + it.Address + it.Cname + it.Value})
			w.WriteHeader(204)
			return
		}
		if r.Method == "DELETE" {
			var items []item
			readJSON(r, &items)
			for _, it := range items {
				st.set(r.PathValue("zone"), it.Name, it.Type, nil)
			}
			w.WriteHeader(204)
			return
		}
		var items []map[string]string
		st.mu.Lock()
		for k, vals := range st.recs {
			parts := strings.Split(k, "|")
			for _, v := range vals {
				f, val, _ := strings.Cut(v, "=")
				items = append(items, map[string]string{"type": parts[2], "name": parts[1], f: val})
			}
		}
		st.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	})

	// Namecheap: getHosts / setHosts over a form POST
	mux.HandleFunc("POST /nc", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		st.mu.Lock()
		defer st.mu.Unlock()
		switch r.Form.Get("Command") {
		case "namecheap.domains.dns.getHosts":
			fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainDNSGetHostsResult Domain="example.com" EmailType="%s">`, st.ncEmail)
			for _, h := range st.ncHosts {
				fmt.Fprintf(w, `<host Name="%s" Type="%s" Address="%s" MXPref="%s" TTL="%s"/>`, h.Name, h.Type, h.Address, h.MXPref, h.TTL)
			}
			fmt.Fprint(w, `</DomainDNSGetHostsResult></CommandResponse></ApiResponse>`)
		case "namecheap.domains.dns.setHosts":
			st.ncEmail = r.Form.Get("EmailType")
			st.ncHosts = nil
			for i := 1; r.Form.Get(fmt.Sprintf("HostName%d", i)) != ""; i++ {
				g := func(k string) string { return r.Form.Get(fmt.Sprintf("%s%d", k, i)) }
				st.ncHosts = append(st.ncHosts, ncHost{Name: g("HostName"), Type: g("RecordType"), Address: g("Address"), MXPref: g("MXPref"), TTL: g("TTL")})
			}
			fmt.Fprint(w, `<ApiResponse Status="OK"/>`)
		}
	})
	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "203.0.113.7\n") })

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
	return st
}

func TestRecordsCreateAndUpdate(t *testing.T) {
	values := map[string]map[string]string{
		"cloudflare": {"api_token": "t"}, "route53": {"access_key_id": "a", "secret_access_key": "s"},
		"porkbun": {"api_key": "k", "api_secret_key": "s"}, "gandi": {"bearer_token": "t"},
		"godaddy": {"api_key": "k", "api_secret": "s"}, "spaceship": {"api_key": "k", "api_secret": "s"},
		"namecheap": {"user": "u", "api_key": "k"},
	}
	ctx := context.Background()
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			st := startRecordAPIs(t)
			v := values[p.ID]
			hc := http.DefaultClient
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "*.lab", "A"); err != nil || len(got) != 0 {
				t.Fatalf("before: %v %v", got, err)
			}
			for _, ip := range []string{"192.168.1.20", "192.168.1.21"} { // create, then update
				if err := p.SetRecord(ctx, hc, v, "example.com", "*.lab", "A", ip); err != nil {
					t.Fatalf("set %s: %v", ip, err)
				}
				got, err := p.GetRecord(ctx, hc, v, "example.com", "*.lab", "A")
				if err != nil || len(got) != 1 || got[0] != ip {
					t.Fatalf("after setting %s: %v %v", ip, got, err)
				}
			}
			switch p.ID {
			case "cloudflare":
				if st.cfComment != ManagedComment {
					t.Error("Cloudflare records should carry the Roostack comment")
				}
			case "namecheap":
				if st.ncEmail != "FWD" || len(st.ncHosts) != 3 {
					t.Errorf("Namecheap rewrite lost data: email %q hosts %+v", st.ncEmail, st.ncHosts)
				}
				if st.ncHosts[0].MXPref != "10" || st.ncHosts[1].Address != "example.com." {
					t.Errorf("existing records changed: %+v", st.ncHosts)
				}
			}
			if ok, risky := p.ManagesRecords(); !ok || risky != (p.ID == "namecheap") {
				t.Errorf("ManagesRecords = %v, %v", ok, risky)
			}
		})
	}
}

// TestRecordTypes: CNAME and TXT records are written in each API's own
// form (its quotes and final dots, Spaceship's field per type), read back
// plain, and deleted; deleting what isn't there is no error.
func TestRecordTypes(t *testing.T) {
	values := map[string]map[string]string{
		"cloudflare": {"api_token": "t"}, "route53": {"access_key_id": "a", "secret_access_key": "s"},
		"porkbun": {"api_key": "k", "api_secret_key": "s"}, "gandi": {"bearer_token": "t"},
		"godaddy": {"api_key": "k", "api_secret": "s"}, "spaceship": {"api_key": "k", "api_secret": "s"},
		"namecheap": {"user": "u", "api_key": "k"},
	}
	// How each API holds them: the CNAME, then the TXT.
	wire := map[string][2]string{
		"cloudflare": {"r-abc.relay.roost.ac", `"roostack-proof=t0k"`},
		"route53":    {"r-abc.relay.roost.ac.", `"roostack-proof=t0k"`},
		"porkbun":    {"r-abc.relay.roost.ac", "roostack-proof=t0k"},
		"gandi":      {"r-abc.relay.roost.ac.", `"roostack-proof=t0k"`},
		"godaddy":    {"r-abc.relay.roost.ac", "roostack-proof=t0k"},
		"spaceship":  {"cname=r-abc.relay.roost.ac", "value=roostack-proof=t0k"},
		"namecheap":  {"r-abc.relay.roost.ac.", "roostack-proof=t0k"},
	}
	ctx := context.Background()
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			st := startRecordAPIs(t)
			v, hc := values[p.ID], http.DefaultClient
			stored := func(name, typ string) string {
				if p.ID == "namecheap" {
					for _, h := range st.ncHosts {
						if h.Name == name && h.Type == typ {
							return h.Address
						}
					}
					return ""
				}
				if got := st.get("example.com", name, typ); len(got) == 1 {
					return got[0]
				}
				return ""
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "vault.lab", "A", "203.0.113.7"); err != nil {
				t.Fatal(err)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "vault.lab", "A"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if got, _ := p.GetRecord(ctx, hc, v, "example.com", "vault.lab", "A"); len(got) != 0 {
				t.Errorf("still there: %v", got)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "vault.lab", "A"); err != nil {
				t.Errorf("deleting what isn't there: %v", err)
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "vault.lab", "CNAME", "r-abc.relay.roost.ac"); err != nil {
				t.Fatal(err)
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT", "roostack-proof=t0k"); err != nil {
				t.Fatal(err)
			}
			if got := stored("vault.lab", "CNAME"); got != wire[p.ID][0] {
				t.Errorf("the CNAME as sent: %q, want %q", got, wire[p.ID][0])
			}
			if got := stored("_roostack.lab", "TXT"); got != wire[p.ID][1] {
				t.Errorf("the TXT as sent: %q, want %q", got, wire[p.ID][1])
			}
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "vault.lab", "CNAME"); err != nil || len(got) != 1 || got[0] != "r-abc.relay.roost.ac" {
				t.Errorf("the CNAME read back: %v %v", got, err)
			}
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT"); err != nil || len(got) != 1 || got[0] != "roostack-proof=t0k" {
				t.Errorf("the TXT read back: %v %v", got, err)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT"); err != nil {
				t.Errorf("deleting the TXT: %v", err)
			}
			if p.ID == "namecheap" && (st.ncEmail != "FWD" || len(st.ncHosts) != 3) {
				t.Errorf("Namecheap's rewrites lost data: %+v", st.ncHosts)
			}
		})
	}
}
