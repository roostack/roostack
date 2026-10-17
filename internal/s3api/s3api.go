// Package s3api makes the few S3 calls Roostack needs beside restic's own
// (SPEC.md §10.10): whether a bucket keeps old versions and for how long,
// putting and deleting an object (the probe that shows a key can't erase
// old versions), and listing and copying back old versions (rolling a
// server's folder back after it was taken over). Plain HTTP, path-style,
// signed with awssig; Backblaze B2's S3-compatible API answers the same
// calls.
package s3api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/awssig"
)

// ErrAccessDenied means the key isn't allowed to do what was asked.
var ErrAccessDenied = errors.New("access denied")

// Client reaches one bucket with one key.
type Client struct {
	// Endpoint is the provider's address (https://s3.example.com).
	Endpoint, Bucket     string
	AccessKey, SecretKey string
	Region               string
	HTTP                 *http.Client
}

// Versioning says whether the bucket keeps old versions of what's
// overwritten or deleted: "Enabled", "Suspended", or "" (never turned on).
func (c *Client) Versioning(ctx context.Context) (string, error) {
	var out struct {
		Status string `xml:"Status"`
	}
	_, err := c.do(ctx, http.MethodGet, "", url.Values{"versioning": {""}}, nil, nil, &out)
	return out.Status, err
}

// lifecycle is the part of a bucket's lifecycle configuration that says
// when old versions are erased.
type lifecycle struct {
	Rules []struct {
		Status string `xml:"Status"`
		// Prefix is the old form of Filter's.
		Prefix *string `xml:"Prefix"`
		Filter *struct {
			Prefix *string   `xml:"Prefix"`
			Tag    *struct{} `xml:"Tag"`
			And    *struct {
				Prefix *string    `xml:"Prefix"`
				Tags   []struct{} `xml:"Tag"`
			} `xml:"And"`
		} `xml:"Filter"`
		NoncurrentVersionExpiration *struct {
			NoncurrentDays int `xml:"NoncurrentDays"`
		} `xml:"NoncurrentVersionExpiration"`
	} `xml:"Rule"`
}

// KeepsOld says how many days the bucket keeps old versions under prefix
// before its lifecycle rules erase them: 0 if it keeps them for good. The
// shortest enabled rule that reaches into prefix counts; a rule only for
// tagged objects doesn't (restic tags nothing).
func (c *Client) KeepsOld(ctx context.Context, prefix string) (int, error) {
	var lc lifecycle
	code, err := c.do(ctx, http.MethodGet, "", url.Values{"lifecycle": {""}}, nil, nil, &lc)
	if code == "NoSuchLifecycleConfiguration" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	days := 0
	for _, r := range lc.Rules {
		if r.Status != "Enabled" || r.NoncurrentVersionExpiration == nil {
			continue
		}
		p := ""
		switch f := r.Filter; {
		case f == nil:
			if r.Prefix != nil {
				p = *r.Prefix
			}
		case f.Tag != nil || f.And != nil && len(f.And.Tags) > 0:
			continue
		case f.Prefix != nil:
			p = *f.Prefix
		case f.And != nil && f.And.Prefix != nil:
			p = *f.And.Prefix
		}
		if !strings.HasPrefix(prefix, p) && !strings.HasPrefix(p, prefix) {
			continue
		}
		if d := max(r.NoncurrentVersionExpiration.NoncurrentDays, 1); days == 0 || d < days {
			days = d
		}
	}
	return days, nil
}

// Put stores an object, and returns its version ("" if the bucket keeps
// no versions).
func (c *Client) Put(ctx context.Context, key string, body []byte) (string, error) {
	h, err := c.request(ctx, http.MethodPut, key, nil, nil, body, nil)
	return h.Get("X-Amz-Version-Id"), err
}

// Delete deletes an object: with versionID "", only hides it in a bucket
// that keeps versions (a delete marker); with one, erases that version.
func (c *Client) Delete(ctx context.Context, key, versionID string) error {
	q := url.Values{}
	if versionID != "" {
		q.Set("versionId", versionID)
	}
	_, err := c.request(ctx, http.MethodDelete, key, q, nil, nil, nil)
	return err
}

// CopyBack makes an old version of an object its newest again, by copying
// it on top: the versions since stay, so it can be undone.
func (c *Client) CopyBack(ctx context.Context, key, versionID string) error {
	src := "/" + c.Bucket + "/" + escapePath(key) + "?versionId=" + url.QueryEscape(versionID)
	_, err := c.request(ctx, http.MethodPut, key, nil, http.Header{"X-Amz-Copy-Source": {src}}, nil, nil)
	return err
}

// Version is one version of an object, or a delete marker (which hides
// the object from then on).
type Version struct {
	Key, VersionID string
	DeleteMarker   bool
	Latest         bool
	Modified       time.Time
	ETag           string
	Size           int64
}

// Versions lists every version and delete marker under prefix, each
// object's newest first, a page at a time.
func (c *Client) Versions(ctx context.Context, prefix string, each func(Version) error) error {
	q := url.Values{"versions": {""}, "prefix": {prefix}, "max-keys": {"1000"}}
	for {
		var page struct {
			Truncated     bool   `xml:"IsTruncated"`
			NextKey       string `xml:"NextKeyMarker"`
			NextVersionID string `xml:"NextVersionIdMarker"`
			// Versions and delete markers, in the answer's order.
			Items []struct {
				XMLName      xml.Name
				Key          string    `xml:"Key"`
				VersionID    string    `xml:"VersionId"`
				IsLatest     bool      `xml:"IsLatest"`
				LastModified time.Time `xml:"LastModified"`
				ETag         string    `xml:"ETag"`
				Size         int64     `xml:"Size"`
			} `xml:",any"`
		}
		if _, err := c.do(ctx, http.MethodGet, "", q, nil, nil, &page); err != nil {
			return err
		}
		for _, it := range page.Items {
			kind := it.XMLName.Local
			if kind != "Version" && kind != "DeleteMarker" {
				continue
			}
			v := Version{Key: it.Key, VersionID: it.VersionID, DeleteMarker: kind == "DeleteMarker", Latest: it.IsLatest,
				Modified: it.LastModified, ETag: strings.Trim(it.ETag, `"`), Size: it.Size}
			if err := each(v); err != nil {
				return err
			}
		}
		if !page.Truncated {
			return nil
		}
		if page.NextKey == "" {
			return errors.New("listing versions: the answer was cut short without saying where to go on")
		}
		q.Set("key-marker", page.NextKey)
		q.Set("version-id-marker", page.NextVersionID)
	}
}

// do makes a request and decodes its XML answer into out (if not nil). It
// returns the error's S3 code, if any, beside the error.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, h http.Header, body []byte, out any) (string, error) {
	var code string
	_, err := c.request(ctx, method, key, q, h, body, func(data []byte) error {
		if out == nil || len(data) == 0 {
			return nil
		}
		return xml.Unmarshal(data, out)
	})
	var se *Error
	if errors.As(err, &se) {
		code = se.Code
	}
	return code, err
}

// Error is an error the provider gave.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	if e.Code != "" {
		return e.Code
	}
	return http.StatusText(e.Status)
}

// Is makes an AccessDenied answer ErrAccessDenied.
func (e *Error) Is(target error) bool {
	return target == ErrAccessDenied && (e.Code == "AccessDenied" || e.Status == http.StatusForbidden)
}

func (c *Client) request(ctx context.Context, method, key string, q url.Values, h http.Header, body []byte,
	decode func([]byte) error) (http.Header, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	u.Path = "/" + c.Bucket
	u.RawPath = "/" + escapePath(c.Bucket)
	if key != "" {
		u.Path += "/" + key
		u.RawPath += "/" + escapePath(key)
	}
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// NewRequest reparses the URL: keep the path exactly as signed.
	req.URL.Path, req.URL.RawPath = u.Path, u.RawPath
	req.ContentLength = int64(len(body))
	if len(body) == 0 {
		req.Body = http.NoBody
	}
	for k, vs := range h {
		req.Header[k] = vs
	}
	sum := sha256.Sum256(body)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	awssig.Sign(req, body, c.AccessKey, c.SecretKey, region, "s3", time.Now())
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		xml.Unmarshal(data, &struct {
			Code    *string `xml:"Code"`
			Message *string `xml:"Message"`
		}{&e.Code, &e.Message})
		return resp.Header, e
	}
	// A copy can fail after its 200 has started (S3 says so in the body).
	if bytes.Contains(data[:min(len(data), 512)], []byte("<Error>")) {
		e := &Error{Status: resp.StatusCode}
		xml.Unmarshal(data, &struct {
			Code    *string `xml:"Code"`
			Message *string `xml:"Message"`
		}{&e.Code, &e.Message})
		return resp.Header, e
	}
	if decode != nil {
		if err := decode(data); err != nil {
			return resp.Header, fmt.Errorf("reading the answer to %s: %w", method, err)
		}
	}
	return resp.Header, nil
}

// escapePath encodes an object's name for its URL the way Signature
// Version 4 wants: everything but letters, digits, "-._~" and "/".
func escapePath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)|0x100, 16)[1:]))
		}
	}
	return b.String()
}
