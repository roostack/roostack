package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// plant writes a file into a folder repository as a taken-over server
// could (named by its SHA-256, as rest-server insists: docs/ASSUMPTIONS.md
// O2), with a name sorting before every name in before (if any).
func plant(t *testing.T, repo, kind string, data func() []byte, before ...string) string {
	t.Helper()
	for range 100000 {
		b := data()
		sum := sha256.Sum256(b)
		name := hex.EncodeToString(sum[:])
		if slices.ContainsFunc(before, func(s string) bool { return name >= s }) {
			continue
		}
		if err := os.WriteFile(filepath.Join(repo, kind, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return name
	}
	t.Fatal("no name found")
	return ""
}

func random(n int) func() []byte {
	return func() []byte {
		b := make([]byte, n)
		rand.Read(b)
		return b
	}
}

func TestCleanJunk(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	if _, err := r.Backup(ctx, BackupOptions{Paths: []string{src}}); err != nil {
		t.Fatal(err)
	}
	keys, err := r.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Junk in each folder; the key sorts first, so restic can't open the
	// repository at all (O4).
	planted := map[string]string{
		"keys":      plant(t, r.URL, "keys", random(300), keys[0].ID),
		"locks":     plant(t, r.URL, "locks", random(200)),
		"snapshots": plant(t, r.URL, "snapshots", random(200)),
		"index":     plant(t, r.URL, "index", random(500)),
	}
	if _, err := r.Snapshots(ctx); err == nil || !LooksLikeJunk(err.Error()) {
		t.Fatalf("with junk: %v, want restic to fail on it", err)
	}

	r.JunkRecheck = time.Millisecond
	files := FilesOf(r)
	if files == nil {
		t.Fatal("no direct access to a folder repository")
	}
	good := map[string]bool{}
	junk, err := CleanJunk(ctx, r, files, good)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, j := range junk {
		if !j.Removed || j.Why == "" || j.Size <= 0 {
			t.Errorf("%+v: want removed, with a reason and its size", j)
		}
		got[j.Kind] = j.Name
	}
	if !maps.Equal(got, planted) {
		t.Fatalf("found %v, want %v", got, planted)
	}
	for kind, name := range planted {
		if _, err := os.Stat(filepath.Join(r.URL, kind, name)); !os.IsNotExist(err) {
			t.Errorf("%s/%s still there: %v", kind, name, err)
		}
	}
	// The repository works again, and its own files were left alone.
	if _, err := r.Backup(ctx, BackupOptions{Paths: []string{src}}); err != nil {
		t.Fatalf("backup after cleaning: %v", err)
	}
	if err := r.Forget(ctx, DefaultRetention); err != nil {
		t.Fatalf("prune after cleaning: %v", err)
	}
	if err := r.Check(ctx, ""); err != nil {
		t.Fatalf("check after cleaning: %v", err)
	}
	if len(good) == 0 {
		t.Error("files read fine weren't remembered")
	}

	// A clean repository has nothing to remove, and a second look reads
	// only what's new.
	if junk, err := CleanJunk(ctx, r, files, good); err != nil || len(junk) != 0 {
		t.Fatalf("clean repository: %v, %v", junk, err)
	}

	// Without direct access only restic's side is looked at, and nothing
	// is removed.
	name := plant(t, r.URL, "snapshots", random(100))
	junk, err = CleanJunk(ctx, r, nil, good)
	if err != nil || len(junk) != 1 || junk[0].Name != name || junk[0].Removed {
		t.Fatalf("without access: %+v, %v", junk, err)
	}
}

func TestBadKey(t *testing.T) {
	key := func(change func(k map[string]any)) []byte {
		k := map[string]any{"kdf": "scrypt", "N": 16384, "r": 8, "p": 14,
			"salt": strings.Repeat("A", 88), "data": strings.Repeat("B", 152)}
		if change != nil {
			change(k)
		}
		b, _ := json.Marshal(k)
		return b
	}
	for _, tc := range []struct {
		name string
		data []byte
		bad  bool
	}{
		{"restic's own", key(nil), false},
		{"many rounds (slow, but restic tries it)", key(func(k map[string]any) { k["N"], k["p"] = 1<<20, 256 }), false},
		{"random bytes", random(300)(), true},
		{"another kdf", key(func(k map[string]any) { k["kdf"] = "argon2" }), true},
		{"N not a power of two", key(func(k map[string]any) { k["N"] = 1000 }), true},
		{"over 1 GiB", key(func(k map[string]any) { k["N"], k["r"] = 1<<20, 16 }), true},
		{"no salt", key(func(k map[string]any) { delete(k, "salt") }), true},
	} {
		if got := badKey(tc.data) != ""; got != tc.bad {
			t.Errorf("%s: bad = %v, want %v (%s)", tc.name, got, tc.bad, badKey(tc.data))
		}
	}
}

// TestRestFiles reads and removes through a rest-server's API, as the
// controller's admin access does.
func TestRestFiles(t *testing.T) {
	name := strings.Repeat("ab", 32)
	files := map[string][]byte{"/lab/srv/keys/" + name: []byte("junk")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "controller" || p != "pw" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/"):
			if r.Header.Get("Accept") != "application/vnd.x.restic.rest.v2" {
				t.Errorf("listing asked for %q", r.Header.Get("Accept"))
			}
			var list []FileInfo
			for p, b := range files {
				if dir, n := filepath.Split(p); dir == r.URL.Path {
					list = append(list, FileInfo{Name: n, Size: int64(len(b))})
				}
			}
			json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodGet:
			b, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		case r.Method == http.MethodDelete:
			delete(files, r.URL.Path)
		}
	}))
	defer srv.Close()
	f := FilesOf(&Repo{URL: "rest:" + srv.URL + "/lab/srv", Env: []string{"RESTIC_REST_USERNAME=controller", "RESTIC_REST_PASSWORD=pw"}})
	ctx := context.Background()
	list, err := f.List(ctx, "keys")
	if err != nil || len(list) != 1 || list[0].Name != name || list[0].Size != 4 {
		t.Fatalf("List: %v, %v", list, err)
	}
	if b, err := f.Read(ctx, "keys", name); err != nil || string(b) != "junk" {
		t.Fatalf("Read: %q, %v", b, err)
	}
	if _, err := f.Read(ctx, "keys", "../config"); err == nil {
		t.Error("read a file that isn't restic's")
	}
	if err := f.Remove(ctx, "keys", name); err != nil || len(files) != 0 {
		t.Fatalf("Remove: %v, left %v", err, files)
	}
	if FilesOf(&Repo{URL: "s3:https://s3.example.com/bucket/roostack"}) != nil {
		t.Error("direct access to online storage")
	}
}
