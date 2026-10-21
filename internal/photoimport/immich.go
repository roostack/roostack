package photoimport

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Roostack migrates photos into the registry's Immich package, which it
// knows (docs/IMMICH-IMPORT.md §10: no package-format change, "It's
// explicit to Immich"): its name, the storage holding the photos, and its
// web route, whose container answers Immich's API.
const (
	PackageName    = "immich"
	LibraryStorage = "library"
)

// The migration tool (build/immich-go): its version and the image
// Roostack builds from it on the server.
const (
	ToolVersion  = "v0.32.0"
	ToolImage    = "localhost/roostack-immich-go:" + ToolVersion
	ToolBuildDir = "/usr/share/roostack/immich-go"
)

// immich-go keeps a list of every photo in the export and in the account
// until it ends (docs/ASSUMPTIONS.md X11, X14): about 1.5 KB each, so a
// library's needs grow with it. It may use a share of the server's
// memory, the Owner's choice (Memory), at least MinMemory, and Go's
// collector is told to keep it under three quarters of that (GoMemLimit),
// which nearly halves what it uses beyond its list. Past the limit the
// kernel stops it, and it says so (SPEC.md §8.5).
const (
	MemoryHalf = "half"
	MemoryMost = "most"
	MinMemory  = 1 << 30
)

// MemoryShares are the choices of memory, as a fraction of the server's.
var MemoryShares = map[string]float64{MemoryHalf: 0.5, MemoryMost: 0.75}

// Memory is what immich-go may use on a server with total bytes of
// memory, given the Owner's choice ("" is half).
func Memory(total int64, share string) int64 {
	f, ok := MemoryShares[share]
	if !ok {
		f = MemoryShares[MemoryHalf]
	}
	return max(MinMemory, int64(float64(total)*f))
}

// GoMemLimit is GOMEMLIMIT for a memory limit: Go's collector works harder
// as the program nears it, leaving room for what isn't Go's heap.
func GoMemLimit(limit int64) string { return fmt.Sprintf("%dMiB", limit*3/4>>20) }

// PhotosPerGB is a rough guide to the library a GB lets immich-go
// migrate (X14: about 1.5 KB a photo, for each photo in the export and
// again once it's in Immich, under GoMemLimit), kept low for real
// libraries' longer names and albums.
const PhotosPerGB = 150_000

// Permissions are what a key needs (immich-go's docs/installation.md at
// v0.32.0): reading what's there, uploading, albums, stacks and tags.
// asset.delete is for replacing a smaller copy on Immich with a bigger one.
var Permissions = []string{
	"asset.read", "asset.statistics", "asset.update", "asset.upload", "asset.copy", "asset.delete", "asset.download",
	"album.create", "album.read", "albumAsset.create", "server.about", "stack.create", "tag.asset", "tag.create", "user.read",
}

// Supports says whether photos can be migrated into a service of this
// package.
func Supports(p *pkgspec.Package) bool {
	if p == nil || p.Name != PackageName || p.Storage[LibraryStorage] == nil {
		return false
	}
	c, _ := Server(p)
	return c != ""
}

// Server is the container answering Immich's API and its port: its web
// route's.
func Server(p *pkgspec.Package) (container string, port int) {
	if r := p.Routes["web"]; r != nil {
		return r.Container, r.Port
	}
	for _, name := range slices.Sorted(maps.Keys(p.Routes)) {
		return p.Routes[name].Container, p.Routes[name].Port
	}
	return "", 0
}

// AccountScript asks Immich, from inside its own container, about a key:
// whose it is, what it may do, and how many photos and videos the account
// has. The key is in API_KEY and the port in PORT (environment, never the
// command line); it prints one JSON object, each path's status and
// answer.
const AccountScript = `const http = require('node:http')
const get = (path) => new Promise((resolve) => {
  const req = http.request({ host: '127.0.0.1', port: Number(process.env.PORT), path, method: 'GET',
    headers: { 'x-api-key': process.env.API_KEY, Accept: 'application/json' } }, (res) => {
    let out = ''
    res.on('data', (c) => { out += c })
    res.on('end', () => { let json = null; try { json = JSON.parse(out) } catch {}; resolve({ status: res.statusCode, json }) })
  })
  req.on('error', (e) => resolve({ status: 0, error: e.message }))
  req.end()
})
;(async () => {
  const out = {}
  for (const p of ['/api/api-keys/me', '/api/users/me', '/api/assets/statistics']) out[p] = await get(p)
  process.stdout.write(JSON.stringify(out))
})()`

// Account is what Immich said about a key.
type Account struct {
	ID, Name, Email string
	Photos, Videos  int64
	// Missing are the permissions it lacks; All: it may do everything.
	Missing []string
	All     bool
}

// answer is one path's answer from AccountScript.
type answer struct {
	Status int             `json:"status"`
	JSON   json.RawMessage `json:"json"`
	Error  string          `json:"error"`
}

// KeyError is something wrong with a key, said for the person who pasted
// it.
type KeyError struct{ Msg string }

func (e *KeyError) Error() string { return e.Msg }

// ParseAccount reads AccountScript's output.
func ParseAccount(out []byte) (Account, error) {
	var answers map[string]answer
	if err := json.Unmarshal(out, &answers); err != nil {
		return Account{}, fmt.Errorf("reading Immich's answer: %w", err)
	}
	key, user, stats := answers["/api/api-keys/me"], answers["/api/users/me"], answers["/api/assets/statistics"]
	switch {
	case key.Status == 0:
		return Account{}, fmt.Errorf("Immich isn't answering (%s): is it running?", strings.TrimSpace(key.Error))
	case key.Status == 401:
		return Account{}, &KeyError{"Immich didn't accept that key. Copy it again: Immich shows a key only once, so you may need to make another."}
	case key.Status != 200:
		return Account{}, fmt.Errorf("Immich answered %d about the key", key.Status)
	}
	var k struct {
		Permissions []string `json:"permissions"`
	}
	json.Unmarshal(key.JSON, &k)
	var a Account
	if slices.Contains(k.Permissions, "all") {
		a.All = true
	} else {
		for _, p := range Permissions {
			if !slices.Contains(k.Permissions, p) {
				a.Missing = append(a.Missing, p)
			}
		}
	}
	if user.Status == 200 {
		var u struct{ ID, Name, Email string }
		json.Unmarshal(user.JSON, &u)
		a.ID, a.Name, a.Email = u.ID, u.Name, u.Email
	}
	if stats.Status == 200 {
		var s struct{ Images, Videos int64 }
		json.Unmarshal(stats.JSON, &s)
		a.Photos, a.Videos = s.Images, s.Videos
	}
	return a, nil
}

// Readable makes one of immich-go's JSON log lines readable: when, what
// happened, to which file, and why. Lines that aren't JSON stay as they
// are.
func Readable(line string) string {
	var l map[string]any
	if json.Unmarshal([]byte(line), &l) != nil {
		return line
	}
	// The time of day, on the server's clock like the rest of the page
	// (immich-go's container is on UTC).
	at, _ := l["time"].(string)
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
		at = t.Local().Format("15:04:05")
	} else if len(at) >= 19 {
		at = at[11:19]
	}
	msg, _ := l["msg"].(string)
	var b strings.Builder
	b.WriteString(at)
	if lvl, _ := l["level"].(string); lvl == "ERROR" || lvl == "WARN" {
		b.WriteString(" " + lvl)
	}
	if msg != "" {
		b.WriteString(" " + msg)
	}
	for _, k := range slices.Sorted(maps.Keys(l)) {
		if k == "time" || k == "level" || k == "msg" || strings.Contains(strings.ToLower(k), "key") {
			continue
		}
		b.WriteString(fmt.Sprintf(" %s=%v", k, l[k]))
	}
	return strings.TrimSpace(b.String())
}
