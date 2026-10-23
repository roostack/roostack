// Package dnstest fakes the record APIs of every DNS provider Roostack
// supports (internal/dns), on one test server, for tests in this package
// and others (internal/dnsrecords). It can't import internal/dns, whose
// own tests use it.
package dnstest

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Paths is where each provider's API is on the fake server, by its real
// address (internal/dns's, which a test there keeps in step).
var Paths = map[string]string{
	"https://api.cloudflare.com/client/v4":   "/cf",
	"https://route53.amazonaws.com":          "/r53",
	"https://api.porkbun.com/api/json/v3":    "/pb",
	"https://api.gandi.net/v5/livedns":       "/gandi",
	"https://api.godaddy.com":                "/gd",
	"https://spaceship.dev/api":              "/ss",
	"https://api.namecheap.com/xml.response": "/nc",
	"https://icanhazip.com":                  "/ip",
}

// prefixes are the providers' paths on the fake server, by provider ID.
var prefixes = map[string]string{
	"cloudflare": "/cf", "route53": "/r53", "porkbun": "/pb", "gandi": "/gandi",
	"godaddy": "/gd", "spaceship": "/ss", "namecheap": "/nc",
}

// Client sends requests for the providers' real addresses to the fake.
func (s *Fake) Client() *http.Client {
	return &http.Client{Transport: rewrite{s}}
}

type rewrite struct{ s *Fake }

func (rt rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL.String()
	for real, path := range Paths {
		if rest, ok := strings.CutPrefix(u, real); ok {
			to, err := url.Parse(rt.s.URL + path + rest)
			if err != nil {
				return nil, err
			}
			r = r.Clone(r.Context())
			r.URL, r.Host = to, to.Host
			return http.DefaultTransport.RoundTrip(r)
		}
	}
	return nil, fmt.Errorf("dnstest: no fake for %s", u)
}

// Refuse makes a provider's API refuse every request, as when its login
// no longer works (true), or work again (false).
func (s *Fake) Refuse(provider string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[prefixes[provider]] = on
}

// FailWrites makes a provider's API fail changes (creating, setting or
// deleting a record) after the next `after` of them, as when it goes
// down, while it still answers reads; after < 0 makes it work again.
func (s *Fake) FailWrites(provider string, after int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if after < 0 {
		delete(s.failing, prefixes[provider])
		return
	}
	s.failing[prefixes[provider]] = after
}

// Requests is every request the fake APIs have had, in order, each as
// its method, path and query on the fake server, the credentials it gave
// in its headers (an AWS signature's key ID) and its body.
func (s *Fake) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// log notes each request (Requests).
func (s *Fake) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		line := r.Method + " " + r.URL.RequestURI()
		if auth := credentials(r); auth != "" {
			line += " auth=" + auth
		}
		if len(body) > 0 {
			line += " " + string(body)
		}
		s.mu.Lock()
		s.requests = append(s.requests, line)
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// credentials are what a request's headers authenticate with.
func credentials(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if sig, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 Credential="); ok {
		key, _, _ := strings.Cut(sig, "/")
		return "AWS4-HMAC-SHA256 " + key // the rest changes with the time
	}
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k + "/" + r.Header.Get("X-API-Secret")
	}
	return auth
}

// check answers refused and failing requests before the APIs see them.
func (s *Fake) check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/" + strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		write := isWrite(prefix, r)
		s.mu.Lock()
		refused := s.refused[prefix]
		left, limited := s.failing[prefix]
		if limited && write && left > 0 {
			s.failing[prefix] = left - 1
		}
		s.mu.Unlock()
		if refused && prefix == "/nc" {
			// Namecheap answers 200, with its error, to a key or IP it
			// doesn't accept.
			fmt.Fprint(w, `<ApiResponse Status="ERROR"><Errors><Error Number="1011150">Invalid request IP</Error></Errors></ApiResponse>`)
			return
		}
		if refused {
			http.Error(w, `{"message":"invalid credentials"}`, http.StatusForbidden)
			return
		}
		if limited && write && left == 0 {
			http.Error(w, `{"message":"something went wrong"}`, http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// zones answers a request for a zone other than Zone as each API does.
// Cloudflare's and Route 53's fakes look a zone up by name instead.
func zones(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		var zone string
		switch {
		case parts[1] == "pb" && len(parts) > 4:
			zone = parts[4] // /pb/dns/<command>/<zone>/…
		case parts[1] == "gandi" && len(parts) > 3:
			zone = parts[3] // /gandi/domains/<zone>/…
		case parts[1] == "gd" && len(parts) > 4:
			zone = parts[4] // /gd/v1/domains/<zone>/…
		case parts[1] == "ss" && len(parts) > 5:
			zone = parts[5] // /ss/v1/dns/records/<zone>
		case parts[1] == "nc":
			r.ParseForm()
			zone = r.Form.Get("SLD") + "." + r.Form.Get("TLD")
		}
		if zone == "" || zone == Zone {
			next.ServeHTTP(w, r)
			return
		}
		switch parts[1] {
		case "pb":
			http.Error(w, `{"status":"ERROR","message":"Invalid domain."}`, 400)
		case "nc":
			fmt.Fprint(w, `<ApiResponse Status="ERROR"><Errors><Error Number="2019166">Domain not found</Error></Errors></ApiResponse>`)
		default:
			http.Error(w, `{"message":"domain not found"}`, http.StatusNotFound)
		}
	})
}

// isWrite says whether a request changes records, in each API's terms.
func isWrite(prefix string, r *http.Request) bool {
	switch prefix {
	case "/pb":
		return !strings.Contains(r.URL.Path, "/retrieve")
	case "/nc":
		r.ParseForm()
		return r.Form.Get("Command") == "namecheap.domains.dns.setHosts"
	}
	return r.Method != http.MethodGet
}

// Fake is a fake DNS provider backend shared by every fake API:
// (zone, name, type) -> values. Namecheap also keeps other records and the
// domain's email setting, to prove its full rewrite preserves them. Every
// API has the one zone, Zone, and every one but Namecheap's refuses a
// CNAME beside another record of the same name, as DNS does.
type Fake struct {
	// URL is the fake server's; Paths says where each API is under it.
	URL       string
	mu        sync.Mutex
	recs      map[string][]string
	NCHosts   []NCHost
	NCEmail   string
	CFComment string
	refused   map[string]bool
	// failing is how many more changes each API makes before it fails
	// them (absent: none fail).
	failing  map[string]int
	requests []string
}

// Zone is the one zone every fake API has.
const Zone = "example.com"

// NCHost is a Namecheap host record, as its API lists them.
type NCHost struct{ Name, Type, Address, MXPref, TTL string }

func key(zone, name, typ string) string { return zone + "|" + name + "|" + typ }

// Get is a record's values as the API holds them.
func (s *Fake) Get(zone, name, typ string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recs[key(zone, name, typ)]
}

// Set changes a record behind Roostack's back (nil deletes it).
func (s *Fake) Set(zone, name, typ string, vals []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key(zone, name, typ)] = vals
}

// clashes reports whether a record would break DNS's rule that a name
// with a CNAME has no other record.
func (s *Fake) clashes(zone, name, typ string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, vals := range s.recs {
		z, rest, _ := strings.Cut(k, "|")
		n, t, _ := strings.Cut(rest, "|")
		if z == zone && n == name && t != typ && len(vals) > 0 && (t == "CNAME" || typ == "CNAME") {
			return true
		}
	}
	return false
}

// Start runs every provider's fake API until the test ends.
func Start(t testing.TB) *Fake {
	t.Helper()
	st := &Fake{recs: map[string][]string{}, refused: map[string]bool{}, failing: map[string]int{},
		NCEmail: "FWD", NCHosts: []NCHost{{Name: "@", Type: "MX", Address: "mail.example.net.", MXPref: "10", TTL: "1800"},
			{Name: "www", Type: "CNAME", Address: "example.com.", TTL: "1800"}}}
	mux := http.NewServeMux()
	readJSON := func(r *http.Request, v any) { b, _ := io.ReadAll(r.Body); json.Unmarshal(b, v) }
	rel := func(fq string) string { return strings.TrimSuffix(strings.TrimSuffix(fq, "."), ".example.com") }

	// Cloudflare
	mux.HandleFunc("GET /cf/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != Zone {
			fmt.Fprint(w, `{"result":[]}`)
			return
		}
		fmt.Fprint(w, `{"result":[{"id":"z1"}]}`)
	})
	// Record IDs are the name and type, so a DELETE says which.
	mux.HandleFunc("GET /cf/zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		name, typ := rel(r.URL.Query().Get("name")), r.URL.Query().Get("type")
		var res []map[string]string
		for _, v := range st.Get("example.com", name, typ) {
			res = append(res, map[string]string{"id": name + "~" + typ, "content": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"result": res})
	})
	cfWrite := func(w http.ResponseWriter, r *http.Request) {
		var rec map[string]any
		readJSON(r, &rec)
		if st.clashes(Zone, rel(rec["name"].(string)), rec["type"].(string)) {
			http.Error(w, `{"errors":[{"code":81053,"message":"An A, AAAA, or CNAME record with that host already exists."}]}`, 400)
			return
		}
		st.Set("example.com", rel(rec["name"].(string)), rec["type"].(string), []string{rec["content"].(string)})
		st.CFComment, _ = rec["comment"].(string)
		fmt.Fprint(w, `{"result":{"id":"r1"}}`)
	}
	mux.HandleFunc("POST /cf/zones/z1/dns_records", cfWrite)
	mux.HandleFunc("PUT /cf/zones/z1/dns_records/{id}", cfWrite)
	mux.HandleFunc("DELETE /cf/zones/z1/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		name, typ, _ := strings.Cut(r.PathValue("id"), "~")
		st.Set("example.com", name, typ, nil)
		fmt.Fprint(w, `{"result":{}}`)
	})

	// Route 53
	mux.HandleFunc("GET /r53/2013-04-01/hostedzonesbyname", func(w http.ResponseWriter, r *http.Request) {
		// Route 53 lists from the name given: the zone itself, or the next.
		name := "example.com."
		if r.URL.Query().Get("dnsname") != Zone {
			name = "zzz.org."
		}
		fmt.Fprintf(w, `<R><HostedZones><HostedZone><Id>/hostedzone/Z9</Id><Name>%s</Name></HostedZone></HostedZones></R>`, name)
	})
	mux.HandleFunc("GET /r53/2013-04-01/hostedzone/Z9/rrset", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		vals := st.Get("example.com", rel(name), r.URL.Query().Get("type"))
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
			if cur := st.Get("example.com", name, typ); len(cur) != 1 || cur[0] != val.V || m[4] != "300" {
				http.Error(w, "InvalidChangeBatch", 400)
				return
			}
			st.Set("example.com", name, typ, nil)
		} else {
			if st.clashes(Zone, name, typ) {
				http.Error(w, "InvalidChangeBatch", 400)
				return
			}
			st.Set("example.com", name, typ, []string{val.V})
		}
		fmt.Fprint(w, `<ok/>`)
	})

	// Porkbun
	mux.HandleFunc("POST /pb/dns/retrieveByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var recs []map[string]string
		for _, v := range st.Get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
			recs = append(recs, map[string]string{"content": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "SUCCESS", "records": recs})
	})
	mux.HandleFunc("POST /pb/dns/create/{zone}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		readJSON(r, &b)
		if len(st.Get(r.PathValue("zone"), b["name"].(string), b["type"].(string))) > 0 {
			fmt.Fprint(w, `{"status":"ERROR","message":"duplicate"}`)
			return
		}
		if st.clashes(r.PathValue("zone"), b["name"].(string), b["type"].(string)) {
			fmt.Fprint(w, `{"status":"ERROR","message":"a CNAME can't be with other records"}`)
			return
		}
		st.Set(r.PathValue("zone"), b["name"].(string), b["type"].(string), []string{b["content"].(string)})
		fmt.Fprint(w, `{"status":"SUCCESS","id":1}`)
	})
	mux.HandleFunc("POST /pb/dns/deleteByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
		fmt.Fprint(w, `{"status":"SUCCESS"}`)
	})
	mux.HandleFunc("POST /pb/dns/editByNameType/{zone}/{typ}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		readJSON(r, &b)
		st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), []string{b["content"].(string)})
		fmt.Fprint(w, `{"status":"SUCCESS"}`)
	})

	// Gandi and GoDaddy: GET/PUT on the record set
	mux.HandleFunc("/gandi/domains/{zone}/records/{name}/{typ}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var b struct {
				Values []string `json:"rrset_values"`
			}
			readJSON(r, &b)
			if st.clashes(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
				http.Error(w, `{"message":"a CNAME can't be with other records"}`, http.StatusConflict)
				return
			}
			st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), b.Values)
			w.WriteHeader(201)
			return
		}
		if r.Method == "DELETE" {
			if len(st.Get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))) == 0 {
				http.Error(w, `{"message":"not found"}`, 404)
				return
			}
			st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
			w.WriteHeader(204)
			return
		}
		vals := st.Get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))
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
			if st.clashes(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
				http.Error(w, `{"code":"CONFLICTING_RECORD"}`, http.StatusConflict)
				return
			}
			st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), []string{b[0].Data})
			return
		}
		if r.Method == "DELETE" {
			if len(st.Get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"))) == 0 {
				http.Error(w, `{"code":"NOT_FOUND"}`, 404)
				return
			}
			st.Set(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ"), nil)
			w.WriteHeader(204)
			return
		}
		var res []map[string]string
		for _, v := range st.Get(r.PathValue("zone"), r.PathValue("name"), r.PathValue("typ")) {
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
			if !b.Force && len(st.Get(r.PathValue("zone"), it.Name, it.Type)) > 0 || st.clashes(r.PathValue("zone"), it.Name, it.Type) {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			st.Set(r.PathValue("zone"), it.Name, it.Type, []string{field[it.Type] + "=" + it.Address + it.Cname + it.Value})
			w.WriteHeader(204)
			return
		}
		if r.Method == "DELETE" {
			var items []item
			readJSON(r, &items)
			for _, it := range items {
				st.Set(r.PathValue("zone"), it.Name, it.Type, nil)
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
			fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainDNSGetHostsResult Domain="example.com" EmailType="%s">`, st.NCEmail)
			for _, h := range st.NCHosts {
				fmt.Fprintf(w, `<host Name="%s" Type="%s" Address="%s" MXPref="%s" TTL="%s"/>`, h.Name, h.Type, h.Address, h.MXPref, h.TTL)
			}
			fmt.Fprint(w, `</DomainDNSGetHostsResult></CommandResponse></ApiResponse>`)
		case "namecheap.domains.dns.setHosts":
			st.NCEmail = r.Form.Get("EmailType")
			st.NCHosts = nil
			for i := 1; r.Form.Get(fmt.Sprintf("HostName%d", i)) != ""; i++ {
				g := func(k string) string { return r.Form.Get(fmt.Sprintf("%s%d", k, i)) }
				st.NCHosts = append(st.NCHosts, NCHost{Name: g("HostName"), Type: g("RecordType"), Address: g("Address"), MXPref: g("MXPref"), TTL: g("TTL")})
			}
			fmt.Fprint(w, `<ApiResponse Status="OK"/>`)
		}
	})
	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "203.0.113.7\n") })

	srv := httptest.NewServer(st.log(st.check(zones(mux))))
	t.Cleanup(srv.Close)
	st.URL = srv.URL
	return st
}
