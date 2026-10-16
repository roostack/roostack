// Package b2 creates and deletes Backblaze B2 application keys limited to
// one folder of a bucket, so each server's backup credentials reach only
// its own repository (SPEC.md §10.10). Plain HTTP against B2's native API
// (v2); restic itself uses B2's S3-compatible API with the key.
//
// UNTESTED against the real service: written from Backblaze's API
// documentation; the tests use a fake server.
package b2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPI is where accounts are authorised.
const DefaultAPI = "https://api.backblazeb2.com"

// ErrCantCreateKeys means the account's key isn't allowed to create keys
// (it lacks the writeKeys capability), so servers share it instead.
var ErrCantCreateKeys = errors.New("this key can't create other keys (it needs the writeKeys capability)")

// Capabilities are what a server's key may do in its own folder: list,
// read and write, but not deleteFiles (SPEC.md §10.10). Through B2's
// S3-compatible API, writeFiles lets restic delete a file by name, which
// only hides it (a delete marker): restic's lock files go as usual, but
// a server that's taken over can only hide its backups, never erase a
// version. The controller prunes with the account's key, and Backblaze
// erases what's hidden as the bucket's lifecycle rules say (KeepsHidden).
var Capabilities = []string{"listBuckets", "listFiles", "readFiles", "writeFiles"}

// IsB2 reports whether an S3 endpoint is Backblaze B2's.
func IsB2(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.HasSuffix(u.Hostname(), ".backblazeb2.com")
}

// Client talks to B2 with an account's key.
type Client struct {
	KeyID, Key string
	// API is where accounts are authorised (DefaultAPI if empty).
	API  string
	HTTP *http.Client
}

type session struct {
	accountID, apiURL, token string
	capabilities             []string
}

// Key is a new application key.
type Key struct {
	ID, Secret string
}

// CreateKey makes a key for bucket, limited to names starting with prefix
// (a folder, ending in "/").
func (c *Client) CreateKey(ctx context.Context, name, bucket, prefix string) (Key, error) {
	s, err := c.authorize(ctx)
	if err != nil {
		return Key{}, err
	}
	if !contains(s.capabilities, "writeKeys") {
		return Key{}, ErrCantCreateKeys
	}
	b, err := c.bucket(ctx, s, bucket)
	if err != nil {
		return Key{}, err
	}
	var created struct {
		ApplicationKeyID string `json:"applicationKeyId"`
		ApplicationKey   string `json:"applicationKey"`
	}
	err = c.call(ctx, s, "b2_create_key", map[string]any{
		"accountId": s.accountID, "keyName": name, "capabilities": Capabilities,
		"bucketId": b.BucketID, "namePrefix": prefix,
	}, &created)
	if err != nil {
		return Key{}, err
	}
	return Key{ID: created.ApplicationKeyID, Secret: created.ApplicationKey}, nil
}

// KeepsHidden says how many days Backblaze keeps hidden files under prefix
// before erasing them, by the bucket's lifecycle rules: 0 if it keeps
// them for good (no rule erases them). The shortest rule that reaches into
// the folder counts.
func (c *Client) KeepsHidden(ctx context.Context, bucket, prefix string) (int, error) {
	s, err := c.authorize(ctx)
	if err != nil {
		return 0, err
	}
	b, err := c.bucket(ctx, s, bucket)
	if err != nil {
		return 0, err
	}
	days := 0
	for _, r := range b.LifecycleRules {
		reaches := strings.HasPrefix(prefix, r.FileNamePrefix) || strings.HasPrefix(r.FileNamePrefix, prefix)
		if !reaches || r.DaysFromHidingToDeleting == nil {
			continue
		}
		if d := max(*r.DaysFromHidingToDeleting, 1); days == 0 || d < days {
			days = d
		}
	}
	return days, nil
}

// bucketInfo is the part of b2_list_buckets' answer Roostack uses.
type bucketInfo struct {
	BucketID       string `json:"bucketId"`
	LifecycleRules []struct {
		FileNamePrefix string `json:"fileNamePrefix"`
		// DaysFromHidingToDeleting is nil when hidden files are kept.
		DaysFromHidingToDeleting *int `json:"daysFromHidingToDeleting"`
	} `json:"lifecycleRules"`
}

// bucket looks a bucket up by name.
func (c *Client) bucket(ctx context.Context, s session, name string) (bucketInfo, error) {
	var buckets struct {
		Buckets []bucketInfo `json:"buckets"`
	}
	if err := c.call(ctx, s, "b2_list_buckets", map[string]string{"accountId": s.accountID, "bucketName": name}, &buckets); err != nil {
		return bucketInfo{}, err
	}
	if len(buckets.Buckets) == 0 {
		return bucketInfo{}, fmt.Errorf("no bucket %q in this account", name)
	}
	return buckets.Buckets[0], nil
}

// DeleteKey removes a key (e.g. of a server that left the lab).
func (c *Client) DeleteKey(ctx context.Context, id string) error {
	s, err := c.authorize(ctx)
	if err != nil {
		return err
	}
	return c.call(ctx, s, "b2_delete_key", map[string]string{"applicationKeyId": id}, nil)
}

func (c *Client) authorize(ctx context.Context) (session, error) {
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/b2api/v2/b2_authorize_account", nil)
	if err != nil {
		return session{}, err
	}
	req.SetBasicAuth(c.KeyID, c.Key)
	var out struct {
		AccountID          string `json:"accountId"`
		APIURL             string `json:"apiUrl"`
		AuthorizationToken string `json:"authorizationToken"`
		Allowed            struct {
			Capabilities []string `json:"capabilities"`
		} `json:"allowed"`
	}
	if err := c.do(req, &out); err != nil {
		return session{}, fmt.Errorf("signing in to Backblaze: %w", err)
	}
	return session{accountID: out.AccountID, apiURL: out.APIURL, token: out.AuthorizationToken,
		capabilities: out.Allowed.Capabilities}, nil
}

func (c *Client) call(ctx context.Context, s session, op string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/b2api/v2/"+op, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", s.token)
	if err := c.do(req, out); err != nil {
		return fmt.Errorf("Backblaze %s: %w", op, err)
	}
	return nil
}

func (c *Client) do(req *http.Request, out any) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			return fmt.Errorf("%s (%s)", e.Message, e.Code)
		}
		return fmt.Errorf("%s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
