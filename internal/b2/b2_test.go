package b2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeB2 imitates the parts of B2's native API that CreateKey uses.
func fakeB2(t *testing.T, capabilities []string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var created map[string]any
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /b2api/v2/b2_authorize_account", func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := r.BasicAuth()
		if !ok || id != "master-id" || key != "master-key" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": "wrong key"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"accountId": "acct", "apiUrl": srv.URL, "authorizationToken": "tok",
			"allowed": map[string]any{"capabilities": capabilities}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_list_buckets", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("Authorization") != "tok" || in["bucketName"] != "backups" {
			json.NewEncoder(w).Encode(map[string]any{"buckets": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"buckets": []any{map[string]string{"bucketId": "bkt1"}}})
	})
	mux.HandleFunc("POST /b2api/v2/b2_create_key", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&created)
		json.NewEncoder(w).Encode(map[string]string{"applicationKeyId": "k1", "applicationKey": "secret1"})
	})
	mux.HandleFunc("POST /b2api/v2/b2_delete_key", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &created
}

func TestCreateKey(t *testing.T) {
	srv, created := fakeB2(t, []string{"listBuckets", "writeKeys", "deleteKeys"})
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	ctx := context.Background()
	k, err := c.CreateKey(ctx, "mylab-lab1-n-0000000002", "backups", "mylab/lab1/n-0000000002/")
	if err != nil || k.ID != "k1" || k.Secret != "secret1" {
		t.Fatalf("key = %+v, %v", k, err)
	}
	got := *created
	if got["bucketId"] != "bkt1" || got["namePrefix"] != "mylab/lab1/n-0000000002/" || got["keyName"] != "mylab-lab1-n-0000000002" {
		t.Errorf("created with %v", got)
	}
	if _, err := c.CreateKey(ctx, "x", "other", "p/"); err == nil {
		t.Error("unknown bucket")
	}
	if err := c.DeleteKey(ctx, "k1"); err != nil {
		t.Error(err)
	}
	bad := &Client{KeyID: "master-id", Key: "wrong", API: srv.URL}
	if _, err := bad.CreateKey(ctx, "x", "backups", "p/"); err == nil {
		t.Error("wrong key")
	}
}

func TestCantCreateKeys(t *testing.T) {
	srv, _ := fakeB2(t, []string{"listFiles", "readFiles"})
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	if _, err := c.CreateKey(context.Background(), "x", "backups", "p/"); !errors.Is(err, ErrCantCreateKeys) {
		t.Errorf("err = %v", err)
	}
}

func TestIsB2(t *testing.T) {
	if !IsB2("https://s3.us-west-004.backblazeb2.com") || IsB2("https://s3.wasabisys.com") || IsB2("https://evilbackblazeb2.com") {
		t.Error("IsB2")
	}
}
