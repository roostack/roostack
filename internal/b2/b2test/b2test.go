// Package b2test is a fake of the parts of Backblaze B2's native API that
// Roostack uses (internal/b2): authorising a key, listing a bucket with its
// lifecycle rules, and making and deleting keys. It's one fake for b2's
// tests and manager's (quality review T8), and doesn't import b2.
package b2test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Bucket is a bucket the fake lists.
type Bucket struct {
	ID string
	// Rules are its lifecycle rules, as B2 lists them; none is an empty
	// list, as Backblaze answers for a bucket without rules.
	Rules []map[string]any
	// NoRules leaves the rules out of the answer altogether.
	NoRules bool
}

// B2 is the fake. Set Keys and Buckets before the first request.
type B2 struct {
	// URL is the API's address (b2.Client's API).
	URL string
	// Keys are the keys it accepts, "id key" → their capabilities.
	Keys map[string][]string
	// Buckets are its buckets by name.
	Buckets map[string]Bucket

	mu      sync.Mutex
	created []map[string]any
	deleted []string
}

// New starts a fake B2 for the test, with no keys or buckets; it stops
// when the test ends.
func New(t testing.TB) *B2 {
	f := &B2{Keys: map[string][]string{}, Buckets: map[string]Bucket{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /b2api/v2/b2_authorize_account", f.authorize)
	mux.HandleFunc("POST /b2api/v2/b2_list_buckets", f.listBuckets)
	mux.HandleFunc("POST /b2api/v2/b2_create_key", f.createKey)
	mux.HandleFunc("POST /b2api/v2/b2_delete_key", f.deleteKey)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// token is the one authorisation token the fake hands out.
const token = "tok"

func (f *B2) authorize(w http.ResponseWriter, r *http.Request) {
	id, key, _ := r.BasicAuth()
	f.mu.Lock()
	caps, ok := f.Keys[id+" "+key]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": "wrong key"})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"accountId": "acct", "apiUrl": f.URL, "authorizationToken": token,
		"allowed": map[string]any{"capabilities": caps}})
}

// listBuckets answers with the bucket asked for by name, or none.
func (f *B2) listBuckets(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	b, ok := f.Buckets[in["bucketName"]]
	f.mu.Unlock()
	if r.Header.Get("Authorization") != token || !ok {
		json.NewEncoder(w).Encode(map[string]any{"buckets": []any{}})
		return
	}
	out := map[string]any{"bucketId": b.ID, "lifecycleRules": b.Rules}
	if b.Rules == nil {
		out["lifecycleRules"] = []any{}
	}
	if b.NoRules {
		delete(out, "lifecycleRules")
	}
	json.NewEncoder(w).Encode(map[string]any{"buckets": []any{out}})
}

// createKey records what it was asked for, and answers with the next key:
// scoped-1 (its secret scoped-1-secret), scoped-2, and so on.
func (f *B2) createKey(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.created = append(f.created, in)
	id := fmt.Sprintf("scoped-%d", len(f.created))
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]string{"applicationKeyId": id, "applicationKey": id + "-secret"})
}

func (f *B2) deleteKey(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.deleted = append(f.deleted, in["applicationKeyId"])
	f.mu.Unlock()
	w.Write([]byte("{}"))
}

// Created are the requests to make a key, in order.
func (f *B2) Created() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.created...)
}

// Deleted are the IDs of the keys deleted, in order.
func (f *B2) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}
