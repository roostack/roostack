package awssig

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The credentials and time of AWS's Signature Version 4 test suite.
const (
	testKey    = "AKIDEXAMPLE"
	testSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

var testTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// Signatures from AWS's SigV4 test suite (get-vanilla and
// get-vanilla-query-order-key-case), which sign host and x-amz-date only,
// as Sign does for a request without a body.
func TestSignAWSTestSuite(t *testing.T) {
	for _, tc := range []struct {
		name, url, want string
	}{
		{"get-vanilla", "https://example.amazonaws.com/",
			"5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		// Parameters are signed sorted, whatever order they're sent in.
		{"get-vanilla-query-order-key-case", "https://example.amazonaws.com/?Param2=value2&Param1=value1",
			"b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		Sign(req, nil, testKey, testSecret, "us-east-1", "service", testTime)
		want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
			"SignedHeaders=host;x-amz-date, Signature=" + tc.want
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
		if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
			t.Errorf("%s: X-Amz-Date = %q", tc.name, got)
		}
	}
}

// SigV4 encodes spaces in parameters as %20 (not +) and sorts repeated
// values.
func TestCanonicalQuery(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/?b=2&a=x+y&b=1&c=%2F", nil)
	if got, want := canonicalQuery(req.URL.Query()), "a=x%20y&b=1&b=2&c=%2F"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// With a body, its length and any x-amz-* headers are signed too.
func TestSignSignsBodyHeaders(t *testing.T) {
	body := []byte(`<xml/>`)
	req, _ := http.NewRequest(http.MethodPost, "https://route53.amazonaws.com/2013-04-01/hostedzone", bytes.NewReader(body))
	req.Header.Set("X-Amz-Content-Sha256", sha256Hex(body))
	Sign(req, body, testKey, testSecret, "us-east-1", "route53", testTime)
	if auth := req.Header.Get("Authorization"); !strings.Contains(auth,
		"SignedHeaders=content-length;host;x-amz-content-sha256;x-amz-date,") {
		t.Errorf("signed headers: %s", auth)
	}
}
