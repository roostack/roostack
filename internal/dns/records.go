package dns

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ManagedComment marks records MyLab created, where the provider supports
// comments (Cloudflare). Ownership is also tracked in MyLab's own state.
const ManagedComment = "Managed by MyLab"

// recordsAPI reads and writes single records. name is relative to zone
// ("*.lab" in zone "example.com"; "*" for the zone itself). Only A records
// are needed so far.
type recordsAPI struct {
	get    func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error)
	upsert func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error
	// risky marks APIs that rewrite every record in the zone to change
	// one (Namecheap); MyLab only uses them after an explicit opt-in.
	risky bool
}

// ManagesRecords reports whether MyLab can manage DNS records with this
// provider, and whether doing so is risky (needs an explicit opt-in).
func (p *Provider) ManagesRecords() (ok, risky bool) {
	if p.records == nil {
		return false, false
	}
	return true, p.records.risky
}

// CanManageRecords reports whether MyLab can manage records here (for templates).
func (p *Provider) CanManageRecords() bool { ok, _ := p.ManagesRecords(); return ok }

// RiskyRecords reports whether managing records needs an opt-in (for templates).
func (p *Provider) RiskyRecords() bool { _, risky := p.ManagesRecords(); return risky }

// GetRecord returns the current values of a record (none if it doesn't exist).
func (p *Provider) GetRecord(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
	if p.records == nil {
		return nil, errors.New(p.Title + " record management isn't supported")
	}
	return p.records.get(ctx, hc, v, zone, name, typ)
}

// SetRecord creates or replaces a record with a single value.
func (p *Provider) SetRecord(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
	if p.records == nil {
		return errors.New(p.Title + " record management isn't supported")
	}
	return p.records.upsert(ctx, hc, v, zone, name, typ, value)
}

// fqdn joins a zone-relative name and the zone.
func fqdn(zone, name string) string {
	if name == "" || name == "@" {
		return zone
	}
	return name + "." + zone
}

// errNotFound lets get functions treat a 404 as "no record".
func isNotFound(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "404 ")
}

// --- Cloudflare ---

func cloudflareZoneID(ctx context.Context, hc *http.Client, auth map[string]string, zone string) (string, error) {
	var zones struct {
		Result []struct{ ID string } `json:"result"`
	}
	if err := call(ctx, hc, "GET", cloudflareAPI+"/zones?name="+url.QueryEscape(zone), auth, nil, &zones); err != nil {
		return "", err
	}
	if len(zones.Result) == 0 {
		return "", errors.New("zone not found")
	}
	return zones.Result[0].ID, nil
}

type cfRecord struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func cloudflareFind(ctx context.Context, hc *http.Client, auth map[string]string, zone, name, typ string) (string, []cfRecord, error) {
	zoneID, err := cloudflareZoneID(ctx, hc, auth, zone)
	if err != nil {
		return "", nil, err
	}
	var recs struct {
		Result []cfRecord `json:"result"`
	}
	q := url.Values{"type": {typ}, "name": {fqdn(zone, name)}}
	err = call(ctx, hc, "GET", cloudflareAPI+"/zones/"+zoneID+"/dns_records?"+q.Encode(), auth, nil, &recs)
	return zoneID, recs.Result, err
}

var cloudflareRecords = &recordsAPI{
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		_, recs, err := cloudflareFind(ctx, hc, map[string]string{"Authorization": "Bearer " + v["api_token"]}, zone, name, typ)
		var out []string
		for _, r := range recs {
			out = append(out, r.Content)
		}
		return out, err
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		auth := map[string]string{"Authorization": "Bearer " + v["api_token"]}
		zoneID, recs, err := cloudflareFind(ctx, hc, auth, zone, name, typ)
		if err != nil {
			return err
		}
		rec := map[string]any{"type": typ, "name": fqdn(zone, name), "content": value, "ttl": 300,
			"proxied": false, "comment": ManagedComment}
		if len(recs) > 0 {
			return call(ctx, hc, "PUT", cloudflareAPI+"/zones/"+zoneID+"/dns_records/"+recs[0].ID, auth, rec, nil)
		}
		return call(ctx, hc, "POST", cloudflareAPI+"/zones/"+zoneID+"/dns_records", auth, rec, nil)
	},
}

// --- Route 53 ---

func route53ZoneID(ctx context.Context, hc *http.Client, v map[string]string, zone string) (string, error) {
	var list struct {
		Zones []struct {
			ID   string `xml:"Id"`
			Name string `xml:"Name"`
		} `xml:"HostedZones>HostedZone"`
	}
	u := route53API + "/2013-04-01/hostedzonesbyname?dnsname=" + url.QueryEscape(zone) + "&maxitems=1"
	if err := signedCall(ctx, hc, "GET", u, nil, v["access_key_id"], v["secret_access_key"], &list); err != nil {
		return "", err
	}
	if len(list.Zones) == 0 || list.Zones[0].Name != zone+"." {
		return "", errors.New("no hosted zone with that name")
	}
	return list.Zones[0].ID, nil
}

var route53Records = &recordsAPI{
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		id, err := route53ZoneID(ctx, hc, v, zone)
		if err != nil {
			return nil, err
		}
		var res struct {
			Sets []struct {
				Name   string   `xml:"Name"`
				Type   string   `xml:"Type"`
				Values []string `xml:"ResourceRecords>ResourceRecord>Value"`
			} `xml:"ResourceRecordSets>ResourceRecordSet"`
		}
		want := fqdn(zone, name) + "."
		u := route53API + "/2013-04-01" + id + "/rrset?name=" + url.QueryEscape(want) + "&type=" + typ + "&maxitems=1"
		if err := signedCall(ctx, hc, "GET", u, nil, v["access_key_id"], v["secret_access_key"], &res); err != nil {
			return nil, err
		}
		for _, s := range res.Sets {
			// Route 53 returns "*" as the octal escape \052.
			if strings.ReplaceAll(s.Name, `\052`, "*") == want && s.Type == typ {
				return s.Values, nil
			}
		}
		return nil, nil
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		id, err := route53ZoneID(ctx, hc, v, zone)
		if err != nil {
			return err
		}
		body := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">` +
			`<ChangeBatch><Comment>` + ManagedComment + `</Comment><Changes><Change><Action>UPSERT</Action><ResourceRecordSet>` +
			`<Name>` + fqdn(zone, name) + `.</Name><Type>` + typ + `</Type><TTL>300</TTL>` +
			`<ResourceRecords><ResourceRecord><Value>` + value + `</Value></ResourceRecord></ResourceRecords>` +
			`</ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`)
		return signedCall(ctx, hc, "POST", route53API+"/2013-04-01"+id+"/rrset", body, v["access_key_id"], v["secret_access_key"], nil)
	},
}

// --- Porkbun ---

func porkbunGet(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
	var res struct {
		porkbunResult
		Records []struct {
			Content string `json:"content"`
		} `json:"records"`
	}
	creds := map[string]any{"apikey": v["api_key"], "secretapikey": v["api_secret_key"]}
	if err := call(ctx, hc, "POST", porkbunAPI+"/dns/retrieveByNameType/"+zone+"/"+typ+"/"+name, nil, creds, &res); err != nil {
		return nil, err
	}
	if res.Status != "SUCCESS" {
		return nil, errors.New(res.Message)
	}
	var out []string
	for _, r := range res.Records {
		out = append(out, r.Content)
	}
	return out, nil
}

var porkbunRecords = &recordsAPI{
	get: porkbunGet,
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		existing, err := porkbunGet(ctx, hc, v, zone, name, typ)
		if err != nil {
			return err
		}
		body := map[string]any{"apikey": v["api_key"], "secretapikey": v["api_secret_key"], "content": value, "ttl": "600"}
		path := "/dns/editByNameType/" + zone + "/" + typ + "/" + name
		if len(existing) == 0 {
			body["name"], body["type"] = name, typ
			path = "/dns/create/" + zone
		}
		var res porkbunResult
		if err := call(ctx, hc, "POST", porkbunAPI+path, nil, body, &res); err != nil {
			return err
		}
		if res.Status != "SUCCESS" {
			return errors.New(res.Message)
		}
		return nil
	},
}

// --- Gandi (PUT replaces the record set) ---

var gandiRecords = &recordsAPI{
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		var res struct {
			Values []string `json:"rrset_values"`
		}
		err := call(ctx, hc, "GET", gandiAPI+"/domains/"+zone+"/records/"+name+"/"+typ,
			map[string]string{"Authorization": "Bearer " + v["bearer_token"]}, nil, &res)
		if isNotFound(err) {
			return nil, nil
		}
		return res.Values, err
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		return call(ctx, hc, "PUT", gandiAPI+"/domains/"+zone+"/records/"+name+"/"+typ,
			map[string]string{"Authorization": "Bearer " + v["bearer_token"]},
			map[string]any{"rrset_values": []string{value}, "rrset_ttl": 300}, nil)
	},
}

// --- GoDaddy (PUT replaces the record set) ---

var godaddyRecords = &recordsAPI{
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		var res []struct {
			Data string `json:"data"`
		}
		err := call(ctx, hc, "GET", godaddyAPI+"/v1/domains/"+zone+"/records/"+typ+"/"+name,
			map[string]string{"Authorization": "sso-key " + v["api_key"] + ":" + v["api_secret"]}, nil, &res)
		var out []string
		for _, r := range res {
			out = append(out, r.Data)
		}
		return out, err
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		return call(ctx, hc, "PUT", godaddyAPI+"/v1/domains/"+zone+"/records/"+typ+"/"+name,
			map[string]string{"Authorization": "sso-key " + v["api_key"] + ":" + v["api_secret"]},
			[]map[string]any{{"data": value, "ttl": 600}}, nil)
	},
}

// --- Spaceship (force:true replaces records with the same name and type) ---

var spaceshipRecords = &recordsAPI{
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		auth := map[string]string{"X-API-Key": v["api_key"], "X-API-Secret": v["api_secret"]}
		var out []string
		for skip := 0; ; skip += 500 {
			var res struct {
				Items []struct {
					Type    string `json:"type"`
					Name    string `json:"name"`
					Address string `json:"address"`
					Value   string `json:"value"`
				} `json:"items"`
				Total int `json:"total"`
			}
			u := spaceshipAPI + "/v1/dns/records/" + zone + "?take=500&skip=" + strconv.Itoa(skip)
			if err := call(ctx, hc, "GET", u, auth, nil, &res); err != nil {
				return nil, err
			}
			for _, it := range res.Items {
				if strings.EqualFold(it.Type, typ) && strings.EqualFold(it.Name, name) {
					out = append(out, it.Address+it.Value)
				}
			}
			if len(res.Items) < 500 {
				return out, nil
			}
		}
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		auth := map[string]string{"X-API-Key": v["api_key"], "X-API-Secret": v["api_secret"]}
		item := map[string]any{"type": typ, "name": name, "address": value, "ttl": 300}
		return call(ctx, hc, "PUT", spaceshipAPI+"/v1/dns/records/"+zone, auth,
			map[string]any{"force": true, "items": []any{item}}, nil)
	},
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

func namecheapRequest(ctx context.Context, hc *http.Client, v map[string]string, zone, command string, extra url.Values) (ncHosts, error) {
	var res ncHosts
	ip := v["client_ip"]
	if ip == "" {
		var err error
		if ip, err = publicIP(ctx, hc); err != nil {
			return res, err
		}
	}
	sld, tld, _ := strings.Cut(zone, ".")
	q := url.Values{
		"ApiUser": {v["user"]}, "UserName": {v["user"]}, "ApiKey": {v["api_key"]}, "ClientIp": {ip},
		"Command": {command}, "SLD": {sld}, "TLD": {tld},
	}
	for k, vals := range extra {
		q[k] = vals
	}
	// POST form: setHosts can carry many records.
	err := call(ctx, hc, "POST", namecheapAPI, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/xml",
	}, []byte(q.Encode()), &res)
	if err == nil {
		err = res.err()
	}
	return res, err
}

var namecheapRecords = &recordsAPI{
	risky: true,
	get: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ string) ([]string, error) {
		res, err := namecheapRequest(ctx, hc, v, zone, "namecheap.domains.dns.getHosts", nil)
		var out []string
		for _, h := range res.Result.Hosts {
			if strings.EqualFold(h.Name, name) && strings.EqualFold(h.Type, typ) {
				out = append(out, h.Address)
			}
		}
		return out, err
	},
	upsert: func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, typ, value string) error {
		res, err := namecheapRequest(ctx, hc, v, zone, "namecheap.domains.dns.getHosts", nil)
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
		add(ncHost{Name: name, Type: typ, Address: value, TTL: "300"})
		_, err = namecheapRequest(ctx, hc, v, zone, "namecheap.domains.dns.setHosts", q)
		return err
	},
}

// publicIP asks an external service for this server's public IP (used
// by Namecheap, whose API needs it).
func publicIP(ctx context.Context, hc *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", publicIPURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't find this lab's public IP address; enter it instead (%v)", reachError(err))
	}
	defer resp.Body.Close()
	var b [64]byte
	n, _ := resp.Body.Read(b[:])
	return strings.TrimSpace(string(b[:n])), nil
}
