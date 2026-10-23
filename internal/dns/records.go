package dns

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ManagedComment marks records Roostack created, where the provider supports
// comments (Cloudflare). Ownership is also tracked in Roostack's own state.
const ManagedComment = "Managed by Roostack"

// recordsAPI reads, writes and deletes single records. name is relative
// to the zone ("*.lab" in zone "example.com"; "@" for the zone itself).
// Types: A, CNAME (to Roostack Cloud's relay) and TXT (proving a domain is
// the lab's, and the credentials check). Values are plain here (a CNAME's
// target without its final dot, a TXT's text without quotes); each API
// encodes them as it wants.
type recordsAPI struct {
	get    func(ctx context.Context, c conn, name, typ string) ([]string, error)
	upsert func(ctx context.Context, c conn, name, typ, value string) error
	// remove deletes every record of a name and type (none: no error).
	remove func(ctx context.Context, c conn, name, typ string) error
	// risky marks APIs that rewrite every record in the zone to change
	// one (Namecheap); Roostack only uses them after an explicit opt-in.
	risky bool
}

// ManagesRecords reports whether Roostack can manage DNS records with this
// provider, and whether doing so is risky (needs an explicit opt-in).
func (p *Provider) ManagesRecords() (ok, risky bool) {
	if p.records == nil {
		return false, false
	}
	return true, p.records.risky
}

// CanManageRecords reports whether Roostack can manage records here (for templates).
func (p *Provider) CanManageRecords() bool { ok, _ := p.ManagesRecords(); return ok }

// RiskyRecords reports whether managing records needs an opt-in (for templates).
func (p *Provider) RiskyRecords() bool { _, risky := p.ManagesRecords(); return risky }

// GetRecord returns the current values of a record (none if it doesn't exist).
func (p *Provider) GetRecord(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
	if p.records == nil {
		return nil, errors.New(p.Title + " record management isn't supported")
	}
	vals, err := p.records.get(ctx, conn{hc, v, zone}, name, typ)
	for i := range vals {
		vals[i] = plain(typ, vals[i])
	}
	return vals, err
}

// DeleteRecord removes every record of a name and type (a public address's
// A record before it becomes a CNAME: a name with a CNAME can have no
// other record). None there is no error.
func (p *Provider) DeleteRecord(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) error {
	if !p.CanDeleteRecords() {
		return errors.New(p.Title + " records can't be deleted by Roostack")
	}
	return p.records.remove(ctx, conn{hc, v, zone}, name, typ)
}

// CanDeleteRecords reports whether Roostack can delete records here
// (every provider but Roostack Cloud's, whose records API only reads and
// sets a roost.ac name's records: they go when the name is released).
func (p *Provider) CanDeleteRecords() bool { return p.records != nil && p.records.remove != nil }

// SameValue reports whether two record values are the same, as Roostack
// compares them: a CNAME's target in any case, other values exactly.
func SameValue(typ, a, b string) bool {
	if typ == "CNAME" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// plain is a value as Roostack compares it: a CNAME's target without its
// final dot, a TXT record's text without its quotes.
func plain(typ, v string) string {
	switch typ {
	case "CNAME":
		return strings.TrimSuffix(v, ".")
	case "TXT":
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
		}
	}
	return v
}

// quoted is TXT text in DNS's quotes (for APIs that want zone-file form).
func quoted(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

// dotted is a CNAME target with its final dot (for APIs that want it).
func dotted(typ, v string) string {
	if typ == "CNAME" && !strings.HasSuffix(v, ".") {
		return v + "."
	}
	return v
}

// SetRecord creates or replaces a record with a single value.
func (p *Provider) SetRecord(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
	if p.records == nil {
		return errors.New(p.Title + " record management isn't supported")
	}
	return p.records.upsert(ctx, conn{hc, v, zone}, name, typ, value)
}

// FQDN joins a zone-relative name and the zone ("" or "@" is the zone
// itself).
func FQDN(zone, name string) string {
	if name == "" || name == "@" {
		return zone
	}
	return name + "." + zone
}

// --- Cloudflare ---

func cloudflareAuth(c conn) map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.v["api_token"]}
}

func cloudflareZoneID(ctx context.Context, c conn) (string, error) {
	var zones struct {
		Result []struct{ ID string } `json:"result"`
	}
	if err := c.call(ctx, "GET", cloudflareAPI+"/zones?name="+url.QueryEscape(c.zone), cloudflareAuth(c), nil, &zones); err != nil {
		return "", err
	}
	if len(zones.Result) == 0 {
		return "", errors.New("zone not found (the token needs Zone → Zone → Read)")
	}
	return zones.Result[0].ID, nil
}

type cfRecord struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func cloudflareFind(ctx context.Context, c conn, name, typ string) (string, []cfRecord, error) {
	zoneID, err := cloudflareZoneID(ctx, c)
	if err != nil {
		return "", nil, err
	}
	var recs struct {
		Result []cfRecord `json:"result"`
	}
	q := url.Values{"type": {typ}, "name": {FQDN(c.zone, name)}}
	err = c.call(ctx, "GET", cloudflareAPI+"/zones/"+zoneID+"/dns_records?"+q.Encode(), cloudflareAuth(c), nil, &recs)
	return zoneID, recs.Result, err
}

var cloudflareRecords = &recordsAPI{
	get: func(ctx context.Context, c conn, name, typ string) ([]string, error) {
		_, recs, err := cloudflareFind(ctx, c, name, typ)
		var out []string
		for _, r := range recs {
			out = append(out, r.Content)
		}
		return out, err
	},
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		zoneID, recs, err := cloudflareFind(ctx, c, name, typ)
		if err != nil {
			return err
		}
		if typ == "TXT" {
			value = quoted(value) // as Cloudflare recommends
		}
		rec := map[string]any{"type": typ, "name": FQDN(c.zone, name), "content": value, "ttl": 300,
			"proxied": false, "comment": ManagedComment}
		if len(recs) > 0 {
			return c.call(ctx, "PUT", cloudflareAPI+"/zones/"+zoneID+"/dns_records/"+recs[0].ID, cloudflareAuth(c), rec, nil)
		}
		return c.call(ctx, "POST", cloudflareAPI+"/zones/"+zoneID+"/dns_records", cloudflareAuth(c), rec, nil)
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		zoneID, recs, err := cloudflareFind(ctx, c, name, typ)
		for _, r := range recs {
			if err == nil {
				err = c.call(ctx, "DELETE", cloudflareAPI+"/zones/"+zoneID+"/dns_records/"+r.ID, cloudflareAuth(c), nil, nil)
			}
		}
		return err
	},
}

// --- Route 53 ---

func route53ZoneID(ctx context.Context, c conn) (string, error) {
	var list struct {
		Zones []struct {
			ID   string `xml:"Id"`
			Name string `xml:"Name"`
		} `xml:"HostedZones>HostedZone"`
	}
	u := route53API + "/2013-04-01/hostedzonesbyname?dnsname=" + url.QueryEscape(c.zone) + "&maxitems=1"
	if err := c.signedCall(ctx, "GET", u, nil, &list); err != nil {
		return "", err
	}
	if len(list.Zones) == 0 || list.Zones[0].Name != c.zone+"." {
		return "", errors.New("no hosted zone with that name")
	}
	return list.Zones[0].ID, nil
}

// route53Set is a record set as Route 53 holds it: deleting one needs it
// exactly, TTL and all. name is the full name, without its final dot.
type route53Set struct {
	name, typ, ttl string
	values         []string
}

// route53Get is the hosted zone's ID and a record set (no values if there
// is none).
func route53Get(ctx context.Context, c conn, name, typ string) (string, route53Set, error) {
	set := route53Set{name: FQDN(c.zone, name), typ: typ}
	id, err := route53ZoneID(ctx, c)
	if err != nil {
		return "", set, err
	}
	var res struct {
		Sets []struct {
			Name   string   `xml:"Name"`
			Type   string   `xml:"Type"`
			TTL    string   `xml:"TTL"`
			Values []string `xml:"ResourceRecords>ResourceRecord>Value"`
		} `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	want := set.name + "."
	u := route53API + "/2013-04-01" + id + "/rrset?name=" + url.QueryEscape(want) + "&type=" + typ + "&maxitems=1"
	if err := c.signedCall(ctx, "GET", u, nil, &res); err != nil {
		return id, set, err
	}
	for _, s := range res.Sets {
		// Route 53 returns "*" as the octal escape \052.
		if strings.ReplaceAll(s.Name, `\052`, "*") == want && s.Type == typ {
			set.ttl, set.values = s.TTL, s.Values
			break
		}
	}
	return id, set, nil
}

// route53Change sends one change to a record set in the hosted zone id.
func route53Change(ctx context.Context, c conn, id, action string, set route53Set) error {
	var recs strings.Builder
	for _, val := range set.values {
		recs.WriteString(`<ResourceRecord><Value>` + xmlText(val) + `</Value></ResourceRecord>`)
	}
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">` +
		`<ChangeBatch><Comment>` + ManagedComment + `</Comment><Changes><Change><Action>` + action + `</Action><ResourceRecordSet>` +
		`<Name>` + set.name + `.</Name><Type>` + set.typ + `</Type><TTL>` + set.ttl + `</TTL>` +
		`<ResourceRecords>` + recs.String() + `</ResourceRecords>` +
		`</ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`)
	return c.signedCall(ctx, "POST", route53API+"/2013-04-01"+id+"/rrset", body, nil)
}

// xmlText escapes text for an XML element.
func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

var route53Records = &recordsAPI{
	get: func(ctx context.Context, c conn, name, typ string) ([]string, error) {
		_, set, err := route53Get(ctx, c, name, typ)
		return set.values, err
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		id, set, err := route53Get(ctx, c, name, typ)
		if err != nil || len(set.values) == 0 {
			return err
		}
		// Route 53 deletes a record set only given exactly as it is.
		return route53Change(ctx, c, id, "DELETE", set)
	},
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		id, err := route53ZoneID(ctx, c)
		if err != nil {
			return err
		}
		if typ == "TXT" {
			value = quoted(value) // Route 53 wants TXT in zone-file form
		}
		return route53Change(ctx, c, id, "UPSERT", route53Set{FQDN(c.zone, name), typ, "300", []string{dotted(typ, value)}})
	},
}

// --- Porkbun ---

// porkbunResult is Porkbun's response envelope (it reports errors in the
// body, sometimes with HTTP 200).
type porkbunResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (r porkbunResult) err() error {
	if r.Status != "SUCCESS" {
		return errors.New(r.Message)
	}
	return nil
}

// porkbunBody is a request's body: the credentials, and fields as given.
func porkbunBody(c conn, fields map[string]any) map[string]any {
	body := map[string]any{"apikey": c.v["api_key"], "secretapikey": c.v["api_secret_key"]}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

func porkbunGet(ctx context.Context, c conn, name, typ string) ([]string, error) {
	var res struct {
		porkbunResult
		Records []struct {
			Content string `json:"content"`
		} `json:"records"`
	}
	if err := c.call(ctx, "POST", porkbunAPI+"/dns/retrieveByNameType/"+c.zone+"/"+typ+"/"+name, nil, porkbunBody(c, nil), &res); err != nil {
		return nil, err
	}
	if err := res.err(); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res.Records {
		out = append(out, r.Content)
	}
	return out, nil
}

var porkbunRecords = &recordsAPI{
	get: porkbunGet,
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		existing, err := porkbunGet(ctx, c, name, typ)
		if err != nil {
			return err
		}
		body := porkbunBody(c, map[string]any{"content": value, "ttl": "600"})
		path := "/dns/editByNameType/" + c.zone + "/" + typ + "/" + name
		if len(existing) == 0 {
			body["name"], body["type"] = name, typ
			path = "/dns/create/" + c.zone
		}
		var res porkbunResult
		if err := c.call(ctx, "POST", porkbunAPI+path, nil, body, &res); err != nil {
			return err
		}
		return res.err()
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		existing, err := porkbunGet(ctx, c, name, typ)
		if err != nil || len(existing) == 0 {
			return err
		}
		var res porkbunResult
		if err := c.call(ctx, "POST", porkbunAPI+"/dns/deleteByNameType/"+c.zone+"/"+typ+"/"+name, nil, porkbunBody(c, nil), &res); err != nil {
			return err
		}
		return res.err()
	},
}

// --- Gandi (PUT replaces the record set) ---

func gandiAuth(c conn) map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.v["bearer_token"]}
}

func gandiURL(c conn, name, typ string) string {
	return gandiAPI + "/domains/" + c.zone + "/records/" + name + "/" + typ
}

var gandiRecords = &recordsAPI{
	get: func(ctx context.Context, c conn, name, typ string) ([]string, error) {
		var res struct {
			Values []string `json:"rrset_values"`
		}
		err := c.call(ctx, "GET", gandiURL(c, name, typ), gandiAuth(c), nil, &res)
		if isNotFound(err) {
			return nil, nil
		}
		return res.Values, err
	},
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		// Gandi wants zone-file form: a CNAME's final dot, a TXT's quotes.
		if typ == "TXT" {
			value = quoted(value)
		}
		return c.call(ctx, "PUT", gandiURL(c, name, typ), gandiAuth(c),
			map[string]any{"rrset_values": []string{dotted(typ, value)}, "rrset_ttl": 300}, nil)
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		err := c.call(ctx, "DELETE", gandiURL(c, name, typ), gandiAuth(c), nil, nil)
		if isNotFound(err) {
			return nil
		}
		return err
	},
}

// --- GoDaddy (PUT replaces the record set) ---

func godaddyAuth(c conn) map[string]string {
	return map[string]string{"Authorization": "sso-key " + c.v["api_key"] + ":" + c.v["api_secret"]}
}

func godaddyURL(c conn, name, typ string) string {
	return godaddyAPI + "/v1/domains/" + c.zone + "/records/" + typ + "/" + name
}

var godaddyRecords = &recordsAPI{
	get: func(ctx context.Context, c conn, name, typ string) ([]string, error) {
		var res []struct {
			Data string `json:"data"`
		}
		err := c.call(ctx, "GET", godaddyURL(c, name, typ), godaddyAuth(c), nil, &res)
		var out []string
		for _, r := range res {
			out = append(out, r.Data)
		}
		return out, err
	},
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		return c.call(ctx, "PUT", godaddyURL(c, name, typ), godaddyAuth(c), []map[string]any{{"data": value, "ttl": 600}}, nil)
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		err := c.call(ctx, "DELETE", godaddyURL(c, name, typ), godaddyAuth(c), nil, nil)
		if isNotFound(err) {
			return nil
		}
		return err
	},
}

// --- Spaceship (force:true replaces records with the same name and type) ---

func spaceshipAuth(c conn) map[string]string {
	return map[string]string{"X-API-Key": c.v["api_key"], "X-API-Secret": c.v["api_secret"]}
}

func spaceshipGet(ctx context.Context, c conn, name, typ string) ([]string, error) {
	var out []string
	for skip := 0; ; skip += 500 {
		var res struct {
			Items []struct {
				Type    string `json:"type"`
				Name    string `json:"name"`
				Address string `json:"address"`
				Value   string `json:"value"`
				Cname   string `json:"cname"`
			} `json:"items"`
			Total int `json:"total"`
		}
		u := spaceshipAPI + "/v1/dns/records/" + c.zone + "?take=500&skip=" + strconv.Itoa(skip)
		if err := c.call(ctx, "GET", u, spaceshipAuth(c), nil, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			if strings.EqualFold(it.Type, typ) && strings.EqualFold(it.Name, name) {
				out = append(out, it.Address+it.Value+it.Cname)
			}
		}
		if len(res.Items) < 500 {
			return out, nil
		}
	}
}

var spaceshipRecords = &recordsAPI{
	get: spaceshipGet,
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		return c.call(ctx, "PUT", spaceshipAPI+"/v1/dns/records/"+c.zone, spaceshipAuth(c),
			map[string]any{"force": true, "items": []any{spaceshipItem(name, typ, value)}}, nil)
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		existing, err := spaceshipGet(ctx, c, name, typ)
		if err != nil || len(existing) == 0 {
			return err
		}
		var items []any
		for _, val := range existing {
			items = append(items, spaceshipItem(name, typ, val))
		}
		return c.call(ctx, "DELETE", spaceshipAPI+"/v1/dns/records/"+c.zone, spaceshipAuth(c), items, nil)
	},
}

// spaceshipItem is a record as Spaceship writes it: the value's field
// depends on the type.
func spaceshipItem(name, typ, value string) map[string]any {
	field := map[string]string{"A": "address", "CNAME": "cname", "TXT": "value"}[typ]
	if field == "" {
		field = "address"
	}
	return map[string]any{"type": typ, "name": name, field: value, "ttl": 300}
}

// --- Namecheap (risky: setHosts replaces every record in the zone) ---

type ncHost struct {
	Name    string `xml:"Name,attr"`
	Type    string `xml:"Type,attr"`
	Address string `xml:"Address,attr"`
	MXPref  string `xml:"MXPref,attr"`
	TTL     string `xml:"TTL,attr"`
}

type ncHosts struct {
	Status string `xml:"Status,attr"`
	Errors []struct {
		Number string `xml:"Number,attr"`
		Text   string `xml:",chardata"`
	} `xml:"Errors>Error"`
	Result struct {
		EmailType string   `xml:"EmailType,attr"`
		Hosts     []ncHost `xml:"host"`
	} `xml:"CommandResponse>DomainDNSGetHostsResult"`
}

func (r ncHosts) err() error {
	if r.Status == "OK" {
		return nil
	}
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, strings.TrimSpace(e.Text)+" (error "+e.Number+")")
	}
	if len(msgs) == 0 {
		msgs = []string{"status " + strconv.Quote(r.Status)}
	}
	return errors.New(strings.Join(msgs, "; "))
}

func namecheapRequest(ctx context.Context, c conn, command string, extra url.Values) (ncHosts, error) {
	var res ncHosts
	ip := c.v["client_ip"]
	if ip == "" {
		var err error
		if ip, err = publicIP(ctx, c.hc); err != nil {
			return res, err
		}
	}
	sld, tld, _ := strings.Cut(c.zone, ".")
	q := url.Values{
		"ApiUser": {c.v["user"]}, "UserName": {c.v["user"]}, "ApiKey": {c.v["api_key"]}, "ClientIp": {ip},
		"Command": {command}, "SLD": {sld}, "TLD": {tld},
	}
	for k, vals := range extra {
		q[k] = vals
	}
	// POST form: setHosts can carry many records.
	err := c.call(ctx, "POST", namecheapAPI, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/xml",
	}, []byte(q.Encode()), &res)
	if err == nil {
		err = res.err()
	}
	return res, err
}

var namecheapRecords = &recordsAPI{
	risky: true,
	get: func(ctx context.Context, c conn, name, typ string) ([]string, error) {
		res, err := namecheapRequest(ctx, c, "namecheap.domains.dns.getHosts", nil)
		var out []string
		for _, h := range res.Result.Hosts {
			if strings.EqualFold(h.Name, name) && strings.EqualFold(h.Type, typ) {
				out = append(out, h.Address)
			}
		}
		return out, err
	},
	upsert: func(ctx context.Context, c conn, name, typ, value string) error {
		res, err := namecheapRequest(ctx, c, "namecheap.domains.dns.getHosts", nil)
		if err != nil {
			return err
		}
		// Rewrite every record exactly as it was, replacing only ours.
		q := url.Values{}
		if res.Result.EmailType != "" {
			q.Set("EmailType", res.Result.EmailType)
		}
		n := 0
		add := func(h ncHost) {
			n++
			i := strconv.Itoa(n)
			q.Set("HostName"+i, h.Name)
			q.Set("RecordType"+i, h.Type)
			q.Set("Address"+i, h.Address)
			q.Set("TTL"+i, h.TTL)
			if h.MXPref != "" {
				q.Set("MXPref"+i, h.MXPref)
			}
		}
		for _, h := range res.Result.Hosts {
			if strings.EqualFold(h.Name, name) && strings.EqualFold(h.Type, typ) {
				continue
			}
			add(h)
		}
		add(ncHost{Name: name, Type: typ, Address: dotted(typ, value), TTL: "300"})
		_, err = namecheapRequest(ctx, c, "namecheap.domains.dns.setHosts", q)
		return err
	},
	remove: func(ctx context.Context, c conn, name, typ string) error {
		res, err := namecheapRequest(ctx, c, "namecheap.domains.dns.getHosts", nil)
		if err != nil {
			return err
		}
		q := url.Values{}
		if res.Result.EmailType != "" {
			q.Set("EmailType", res.Result.EmailType)
		}
		n, found := 0, false
		for _, h := range res.Result.Hosts {
			if strings.EqualFold(h.Name, name) && strings.EqualFold(h.Type, typ) {
				found = true
				continue
			}
			n++
			i := strconv.Itoa(n)
			q.Set("HostName"+i, h.Name)
			q.Set("RecordType"+i, h.Type)
			q.Set("Address"+i, h.Address)
			q.Set("TTL"+i, h.TTL)
			if h.MXPref != "" {
				q.Set("MXPref"+i, h.MXPref)
			}
		}
		if !found {
			return nil
		}
		_, err = namecheapRequest(ctx, c, "namecheap.domains.dns.setHosts", q)
		return err
	},
}
