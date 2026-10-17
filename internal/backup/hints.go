package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// Key hints (docs/ASSUMPTIONS.md O5, O6). restic opens a repository by
// trying its key files in turn until one opens with the password, and
// anyone who can add files there (a server backing up, append-only, or the
// server storing another's backups) can add a well-formed key whose scrypt
// parameters take minutes to derive. Every open then crawls, for that
// server and for the controller pruning it. Told which key file to try
// first (RESTIC_KEY_HINT), restic reads no other.
//
// So Roostack remembers, for each repository and password, the ID of the
// key file that opens it: when the repository is made (Init), when a key
// is added (AddKey), when the controller hands a server its key (the
// server's backup settings carry the ID), and otherwise by asking restic
// once (`key list`, the one open that may still be slow). A wrong hint
// costs nothing: restic falls back to trying every key.

// KeyHints is a file of key IDs, by repository and password.
type KeyHints struct {
	// File is the JSON file (HintsFile), 0600.
	File string
}

// HintsFile is where a server keeps its key hints.
func HintsFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "backup-key-hints.json")
}

// Hints returns the server's key hints.
func Hints(paths config.Paths) *KeyHints { return &KeyHints{File: HintsFile(paths)} }

// hintsMu orders this process's writes; another process (the roostack
// command) may lose a race, which only means asking restic again later.
var hintsMu sync.Mutex

// keyID is what restic accepts as a key hint: a key file's ID, in full or
// shortened. Anything else (a hint from another server) is ignored.
var keyID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// hintName identifies a repository and password without naming the
// password: a hash of both (passwords are random, so it can't be guessed).
func hintName(url string, password []byte) string {
	h := sha256.New()
	h.Write([]byte(url))
	h.Write([]byte{0})
	h.Write(bytes.TrimSpace(password))
	return hex.EncodeToString(h.Sum(nil))
}

func (h *KeyHints) read() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(h.File)
	if err == nil {
		json.Unmarshal(data, &m)
	}
	return m
}

// Get returns the key ID for a repository and password, or "".
func (h *KeyHints) Get(url string, password []byte) string {
	id := h.read()[hintName(url, password)]
	if !keyID.MatchString(id) {
		return ""
	}
	return id
}

// Set records the key ID for a repository and password. An ID that isn't
// one is refused.
func (h *KeyHints) Set(url string, password []byte, id string) error {
	if !keyID.MatchString(id) {
		return errors.New("not a restic key ID")
	}
	hintsMu.Lock()
	defer hintsMu.Unlock()
	m := h.read()
	name := hintName(url, password)
	if m[name] == id {
		return nil
	}
	m[name] = id
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(h.File), ".key-hints-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), h.File)
}

// SetFor records the key ID for a repository and the key in keyFile.
func (h *KeyHints) SetFor(url, keyFile, id string) error {
	password, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	return h.Set(url, password, id)
}

// hintRetry is how long after a failed lookup the next one waits (a
// repository not made yet, a drive unplugged), so commands don't each
// pay for an extra `key list`.
const hintRetry = time.Hour

// lookups are this process's failed lookups, by hintName.
var lookups sync.Map

// keyHint is the hint to give restic: the one recorded, or else (at most
// hourly) the ID of the key the password opens, asked of restic and
// recorded. "" when there's none.
func (r *Repo) keyHint(ctx context.Context) string {
	if r.Hints == nil {
		return ""
	}
	password, err := os.ReadFile(r.PasswordFile)
	if err != nil {
		return ""
	}
	if id := r.Hints.Get(r.URL, password); id != "" {
		return id
	}
	name := hintName(r.URL, password)
	if t, ok := lookups.Load(name); ok && time.Since(t.(time.Time)) < hintRetry {
		return ""
	}
	id, err := r.recordHint(ctx)
	if err != nil {
		lookups.Store(name, time.Now())
		return ""
	}
	lookups.Delete(name)
	return id
}

// recordHint asks restic which key the password opens, and records it.
func (r *Repo) recordHint(ctx context.Context) (string, error) {
	bare := *r
	bare.Hints = nil // no lookup within the lookup
	id, err := bare.CurrentKey(ctx)
	if err != nil {
		return "", err
	}
	return id, r.Hints.SetFor(r.URL, r.PasswordFile, id)
}

// KeyID is the ID of the key this Repo opens the repository with, from
// its hints (asking restic if need be), or "" if unknown: for handing a
// server the hint to its repository (SPEC.md §10.10).
func (r *Repo) KeyID(ctx context.Context) string {
	if r.Hints == nil {
		id, _ := r.CurrentKey(ctx)
		return id
	}
	return r.keyHint(ctx)
}
