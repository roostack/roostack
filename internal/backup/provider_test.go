package backup

import "testing"

// Requests are signed for the region the endpoint names.
func TestRegion(t *testing.T) {
	for endpoint, want := range map[string]string{
		"https://s3.us-west-004.backblazeb2.com":  "us-west-004",
		"https://s3.eu-central-1.wasabisys.com":   "eu-central-1",
		"https://s3.ap-southeast-2.amazonaws.com": "ap-southeast-2",
		"https://abc123.r2.cloudflarestorage.com": "auto",
		"https://minio.home.example.com:9000":     "us-east-1",
	} {
		if got := (S3{Endpoint: endpoint}).Region(); got != want {
			t.Errorf("Region(%s) = %q, want %q", endpoint, got, want)
		}
	}
}
