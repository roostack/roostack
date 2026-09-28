package backup

import "strings"

// S3 is an S3-compatible provider (Backblaze B2, Wasabi, AWS, …) that any
// of a lab's servers can back up to, each in its own folder (SPEC.md
// §10.10).
type S3 struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// Clean trims what people paste.
func (s S3) Clean() S3 {
	s.Endpoint = strings.TrimRight(strings.TrimSpace(s.Endpoint), "/")
	s.Bucket = strings.Trim(strings.TrimSpace(s.Bucket), "/")
	s.Prefix = strings.Trim(strings.TrimSpace(s.Prefix), "/")
	s.AccessKey, s.SecretKey = strings.TrimSpace(s.AccessKey), strings.TrimSpace(s.SecretKey)
	return s
}

// Problem is what's missing, for people, or "".
func (s S3) Problem() string {
	switch {
	case !strings.HasPrefix(s.Endpoint, "https://") || s.Bucket == "":
		return "Enter the storage endpoint (starting with https://) and a bucket name."
	case s.AccessKey == "" || s.SecretKey == "":
		return "Enter the access key and secret key."
	}
	return ""
}

// Repository is a server's repository in the bucket.
func (s S3) Repository(labID, serverID string) string {
	return "s3:" + s.Endpoint + "/" + s.Bucket + "/" + ServerFolder(s.Prefix, labID, serverID)
}

// Env is the credentials restic needs.
func (s S3) Env() []string {
	return []string{"AWS_ACCESS_KEY_ID=" + s.AccessKey, "AWS_SECRET_ACCESS_KEY=" + s.SecretKey}
}

// ServerFolder is where a server's repository goes under a provider's
// folder: every server has its own, always (SPEC.md §10.10).
func ServerFolder(base, labID, serverID string) string {
	return strings.Trim(strings.Join([]string{strings.Trim(base, "/"), "roostack", labID, serverID}, "/"), "/")
}
