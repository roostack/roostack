package dns

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"roostack.cloud/roostack/internal/config"
)

// cloudProvider is Roostack Cloud, for a lab whose address is a roost.ac
// name (SPEC.md §6.6). It's never offered in the list: the lab gets its
// credentials by claiming the name. They can only write that name's
// records: its challenge record, through the standard acme-dns API that
// Caddy's acmedns module speaks, and its A records, through Cloud's
// records API.
var cloudProvider = &Provider{
	ID: config.ProviderCloud, Title: "Roostack Cloud", Plugin: "github.com/caddy-dns/acmedns",
	Hidden: true,
	Fields: []Field{
		{Key: "server_url", Label: "Challenge server"},
		{Key: "username", Label: "Username"},
		{Key: "password", Label: "Password", Sensitive: true},
		{Key: "subdomain", Label: "Challenge subdomain"},
		{Key: "records_url", Label: "Records API"},
	},
	directive: func(set []string) string {
		return block("acmedns", set, "username", "password", "subdomain", "server_url")
	},
	records: &recordsAPI{
		get: func(ctx context.Context, hc *http.Client, v map[string]string, _, name, typ string) ([]string, error) {
			var out struct {
				Values []string `json:"values"`
			}
			err := call(ctx, hc, "GET", cloudRecordURL(v, name, typ), cloudAuth(v), nil, &out)
			if isNotFound(err) {
				return nil, nil
			}
			return out.Values, err
		},
		upsert: func(ctx context.Context, hc *http.Client, v map[string]string, _, name, typ, value string) error {
			return call(ctx, hc, "PUT", cloudRecordURL(v, name, typ), cloudAuth(v), map[string]string{"value": value}, nil)
		},
	},
}

func cloudAuth(v map[string]string) map[string]string {
	return map[string]string{"X-Api-User": v["username"], "X-Api-Key": v["password"]}
}

// cloudRecordURL is a record's address in Cloud's records API: its name
// relative to the lab's name ("@" for the name itself) and its type.
func cloudRecordURL(v map[string]string, name, typ string) string {
	if name == "" {
		name = "@"
	}
	return strings.TrimRight(v["records_url"], "/") + "/" + url.PathEscape(name) + "/" + url.PathEscape(typ)
}

func init() { providers = append(providers, cloudProvider) }
