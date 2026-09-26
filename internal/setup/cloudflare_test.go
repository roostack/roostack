package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCloudflare accepts token "good" (and "expired", reported as expired)
// and knows the zone example.com.
func fakeCloudflare(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "good" && token != "expired" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`))
			return
		}
		switch r.URL.Path {
		case "/user/tokens/verify":
			status := "active"
			if token == "expired" {
				status = "expired"
			}
			w.Write([]byte(`{"success":true,"result":{"status":"` + status + `"}}`))
		case "/zones":
			if r.URL.Query().Get("name") == "example.com" {
				w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"example.com"}]}`))
			} else {
				w.Write([]byte(`{"success":true,"result":[]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := CloudflareAPI
	CloudflareAPI = srv.URL
	t.Cleanup(func() { CloudflareAPI = old })
}

func TestCheckCloudflare(t *testing.T) {
	fakeCloudflare(t)
	ctx := context.Background()
	c := http.DefaultClient

	zone, err := CheckCloudflare(ctx, c, " good ", "lab.example.com")
	if err != nil || zone != "example.com" {
		t.Errorf("good token: zone %q, err %v", zone, err)
	}
	for _, tc := range []struct{ token, domain, want string }{
		{"", "lab.example.com", "Enter your"},
		{"bad", "lab.example.com", "didn't accept"},
		{"expired", "lab.example.com", "expired, not active"},
		{"good", "lab.other.org", "can't see a Cloudflare zone for lab.other.org"},
	} {
		_, err := CheckCloudflare(ctx, c, tc.token, tc.domain)
		var ce *CheckError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, tc.want) {
			t.Errorf("token %q domain %q: err %v, want %q", tc.token, tc.domain, err, tc.want)
		}
	}
}

func TestCheckCloudflareUnreachable(t *testing.T) {
	old := CloudflareAPI
	CloudflareAPI = "http://127.0.0.1:1" // nothing listens here
	defer func() { CloudflareAPI = old }()
	_, err := CheckCloudflare(context.Background(), http.DefaultClient, "good", "lab.example.com")
	var ce *CheckError
	if !errors.As(err, &ce) || !strings.Contains(ce.Msg, "Couldn't reach Cloudflare") {
		t.Errorf("err = %v", err)
	}
}
