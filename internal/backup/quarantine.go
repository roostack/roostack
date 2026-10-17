package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Junk in a repository (SPEC.md §10.10, docs/ASSUMPTIONS.md O). An
// append-only store keeps a server that's taken over from deleting or
// overwriting backups, but not from adding files. A file restic can't
// read in locks/ or keys/ stops every command on the repository, and one
// in index/ or snapshots/ stops backups and prunes. Restic itself can't
// remove most of them, so the controller looks for them with its own key
// and removes them through its admin access.

// JunkKinds are the folders searched, in the order they're handled: keys
// first, as a junk key can stop restic opening the repository at all.
// data/ isn't searched: a junk pack does no harm, and the next prune
// deletes it.
var JunkKinds = []string{"keys", "locks", "snapshots", "index"}

// Junk is a file in a repository that restic can't use.
type Junk struct {
	Kind string `json:"kind"` // its folder: keys, locks, snapshots or index
	Name string `json:"name"` // its file name
	Size int64  `json:"size"` // bytes, -1 if unknown
	// Why is restic's error reading it (or why a key isn't one).
	Why string `json:"why"`
	// Removed is set once it's gone from the repository (CleanJunk).
	Removed bool `json:"removed,omitempty"`
}

// Path is the file's path in the repository.
func (j Junk) Path() string { return j.Kind + "/" + j.Name }

// JunkRecheck is how long FindJunk waits before reading a suspect file a
// second time, so a passing network fault isn't mistaken for junk.
var JunkRecheck = time.Minute

// junkError is restic's error for a file not written with the
// repository's key: its authentication check failed. Other errors (the
// network, a file still being written) don't count.
const junkError = "ciphertext verification failed"

// Files is direct access to a repository's files, below restic, for
// removing what restic can't (FilesOf).
type Files interface {
	// List lists a folder's files (keys, locks, snapshots or index).
	List(ctx context.Context, kind string) ([]FileInfo, error)
	// Read reads a file, up to maxKeySize bytes.
	Read(ctx context.Context, kind, name string) ([]byte, error)
	Remove(ctx context.Context, kind, name string) error
}

// FileInfo is a file in a repository folder.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// maxKeySize is as much of a key file as is read: restic's are under
// 1 KB.
const maxKeySize = 64 << 10

// fileName is a restic file name: the SHA-256 of its contents.
var fileName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FilesOf returns direct access to r's files where Roostack can remove
// them, or nil: a rest-server, with r's credentials (the controller's
// admin access, which can delete), or a folder, reached as restic is
// (r.Launch, outside Roostack's sandbox for a drive). Online storage
// isn't reached this way.
func FilesOf(r *Repo) Files {
	switch {
	case strings.HasPrefix(r.URL, "rest:"):
		u, err := url.Parse(strings.TrimPrefix(r.URL, "rest:"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil
		}
		f := &restFiles{base: u, client: &http.Client{Timeout: time.Minute}}
		if u.User != nil {
			f.user = u.User.Username()
			f.password, _ = u.User.Password()
			u.User = nil
		}
		for _, e := range r.Env {
			if v, ok := strings.CutPrefix(e, "RESTIC_REST_USERNAME="); ok {
				f.user = v
			} else if v, ok := strings.CutPrefix(e, "RESTIC_REST_PASSWORD="); ok {
				f.password = v
			}
		}
		return f
	case strings.HasPrefix(r.URL, "/"), strings.HasPrefix(r.URL, "local:/"):
		launch := r.Launch
		if launch == nil {
			launch = DirectLaunch
		}
		return &folderFiles{dir: strings.TrimPrefix(r.URL, "local:"), launch: launch}
	}
	return nil
}

// restFiles is a rest-server repository, through its REST API.
type restFiles struct {
	base           *url.URL
	user, password string
	client         *http.Client
}

func (f *restFiles) do(ctx context.Context, method, p string) (*http.Response, error) {
	u := *f.base
	u.Path = path.Join(u.Path, p)
	if strings.HasSuffix(p, "/") {
		u.Path += "/"
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if f.user != "" {
		req.SetBasicAuth(f.user, f.password)
	}
	// The listing with sizes (rest-server's API v2).
	req.Header.Set("Accept", "application/vnd.x.restic.rest.v2")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %s", method, p, resp.Status)
	}
	return resp, nil
}

func (f *restFiles) List(ctx context.Context, kind string) ([]FileInfo, error) {
	resp, err := f.do(ctx, http.MethodGet, kind+"/")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var files []FileInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&files); err != nil {
		return nil, fmt.Errorf("listing %s: %w", kind, err)
	}
	return files, nil
}

func (f *restFiles) Read(ctx context.Context, kind, name string) ([]byte, error) {
	if !fileName.MatchString(name) {
		return nil, fmt.Errorf("not a restic file name: %q", name)
	}
	resp, err := f.do(ctx, http.MethodGet, kind+"/"+name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxKeySize))
}

func (f *restFiles) Remove(ctx context.Context, kind, name string) error {
	if !fileName.MatchString(name) {
		return fmt.Errorf("not a restic file name: %q", name)
	}
	resp, err := f.do(ctx, http.MethodDelete, kind+"/"+name)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// folderFiles is a repository in a folder, reached with plain commands
// launched as restic is.
type folderFiles struct {
	dir    string
	launch func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd
}

func (f *folderFiles) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := f.launch(ctx, nil, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (f *folderFiles) List(ctx context.Context, kind string) ([]FileInfo, error) {
	out, err := f.run(ctx, "find", path.Join(f.dir, kind), "-maxdepth", "1", "-type", "f", "-printf", "%f %s\n")
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for line := range strings.Lines(string(out)) {
		name, size, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(size, 10, 64)
		files = append(files, FileInfo{Name: name, Size: n})
	}
	return files, nil
}

func (f *folderFiles) Read(ctx context.Context, kind, name string) ([]byte, error) {
	if !fileName.MatchString(name) {
		return nil, fmt.Errorf("not a restic file name: %q", name)
	}
	return f.run(ctx, "head", "-c", strconv.Itoa(maxKeySize), "--", path.Join(f.dir, kind, name))
}

func (f *folderFiles) Remove(ctx context.Context, kind, name string) error {
	if !fileName.MatchString(name) {
		return fmt.Errorf("not a restic file name: %q", name)
	}
	_, err := f.run(ctx, "rm", "-f", "--", path.Join(f.dir, kind, name))
	return err
}

// keyFile is what restic needs of a key file to try it.
type keyFile struct {
	KDF  string `json:"kdf"`
	N    int64  `json:"N"`
	R    int64  `json:"r"`
	P    int64  `json:"p"`
	Salt []byte `json:"salt"`
	Data []byte `json:"data"`
}

// badKey says why data isn't a key restic can try, or "" if it is. Key
// files aren't encrypted: restic stops opening the repository at the
// first it can't parse, or whose scrypt parameters it refuses (more than
// 1 GiB of memory: docs/ASSUMPTIONS.md O4, O5), when that one sorts
// before the real key.
func badKey(data []byte) string {
	var k keyFile
	if err := json.Unmarshal(data, &k); err != nil {
		return "not a restic key: " + err.Error()
	}
	switch {
	case k.KDF != "scrypt":
		return fmt.Sprintf("not a restic key: key derivation %q", k.KDF)
	case k.N <= 1 || k.N&(k.N-1) != 0 || k.R < 1 || k.P < 1 || k.R*k.P >= 1<<30:
		return "not a restic key: impossible scrypt parameters"
	case k.N > (1<<30)/128/k.R:
		return "not a restic key: scrypt parameters restic refuses (over 1 GiB of memory)"
	case len(k.Salt) < 8 || len(k.Data) == 0:
		return "not a restic key: no salt or no data"
	}
	return ""
}

// FindJunk lists the files in r that its key can't read: key files that
// aren't keys (read through files; skipped when files is nil), and lock,
// snapshot and index files that fail restic's authentication check. Each
// suspect is read again after JunkRecheck and counts only if it fails the
// same way. good holds "kind/name" of files already read fine (they never
// change), and gains those read now; nil reads every file.
func FindJunk(ctx context.Context, r *Repo, files Files, good map[string]bool) ([]Junk, error) {
	var suspects []Junk
	sizes := map[string]int64{}
	if files != nil {
		for _, kind := range JunkKinds {
			list, err := files.List(ctx, kind)
			if err != nil {
				return nil, err
			}
			for _, f := range list {
				sizes[kind+"/"+f.Name] = f.Size
			}
		}
		keys, err := keySuspects(ctx, files, sizes)
		if err != nil {
			return nil, err
		}
		suspects = append(suspects, keys...)
	}
	// Restic reads the rest. A junk key would stop it opening the
	// repository, so with one found it waits for RemoveJunk.
	if len(suspects) == 0 {
		found, err := r.junkSuspects(ctx, sizes, good)
		if err != nil {
			return nil, err
		}
		suspects = append(suspects, found...)
	}
	if len(suspects) == 0 {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(JunkRecheck):
	}
	var junk []Junk
	for _, s := range suspects {
		var why string
		if s.Kind == "keys" {
			data, err := files.Read(ctx, s.Kind, s.Name)
			if err != nil {
				continue // gone, or not readable now: not counted
			}
			why = badKey(data)
		} else {
			why = r.unreadable(ctx, s.Kind, s.Name)
		}
		if why != "" {
			s.Why = why
			junk = append(junk, s)
		}
	}
	return junk, nil
}

// keySuspects reads every key file.
func keySuspects(ctx context.Context, files Files, sizes map[string]int64) ([]Junk, error) {
	list, err := files.List(ctx, "keys")
	if err != nil {
		return nil, err
	}
	var out []Junk
	for _, f := range list {
		if !fileName.MatchString(f.Name) {
			continue
		}
		data, err := files.Read(ctx, "keys", f.Name)
		if err != nil {
			return nil, err
		}
		if why := badKey(data); why != "" {
			out = append(out, Junk{Kind: "keys", Name: f.Name, Size: sizeOf(sizes, "keys", f.Name), Why: why})
		}
	}
	return out, nil
}

// catKind is restic cat's name for a folder's files.
var catKind = map[string]string{"locks": "lock", "snapshots": "snapshot", "index": "index"}

// junkSuspects asks restic to read every lock, snapshot and index file
// not in good. Snapshots are read in one go: restic lists the ones it can
// read, and only the others are read one by one.
func (r *Repo) junkSuspects(ctx context.Context, sizes map[string]int64, good map[string]bool) ([]Junk, error) {
	var out []Junk
	for _, kind := range []string{"locks", "snapshots", "index"} {
		// Listed before the names, so a snapshot made in between is
		// read on its own rather than suspected.
		readable := map[string]bool{}
		if kind == "snapshots" {
			out, err := r.run(ctx, "snapshots", "--json", "--no-lock")
			if err != nil {
				return nil, err
			}
			var snaps []Snapshot
			if err := json.Unmarshal(out, &snaps); err != nil {
				return nil, fmt.Errorf("restic snapshots: %w", err)
			}
			for _, s := range snaps {
				readable[s.ID] = true
			}
		}
		names, err := r.list(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if good[kind+"/"+name] || readable[name] {
				markGood(good, kind, name)
				continue
			}
			if why := r.unreadable(ctx, kind, name); why != "" {
				out = append(out, Junk{Kind: kind, Name: name, Size: sizeOf(sizes, kind, name), Why: why})
			} else if kind != "locks" { // a lock goes soon; it isn't worth keeping
				markGood(good, kind, name)
			}
		}
	}
	return out, nil
}

// list lists a folder's file names with restic (no decryption).
func (r *Repo) list(ctx context.Context, kind string) ([]string, error) {
	out, err := r.run(ctx, "list", "--no-lock", "--quiet", kind)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.Lines(string(out)) {
		if name := strings.TrimSpace(line); fileName.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

// unreadable returns restic's error if a file fails its authentication
// check, or "" if it reads (or fails some other way).
func (r *Repo) unreadable(ctx context.Context, kind, name string) string {
	_, err := r.run(ctx, "cat", "--no-lock", "--quiet", catKind[kind], name)
	if err != nil && strings.Contains(err.Error(), junkError) {
		return junkError
	}
	return ""
}

// CleanJunk finds junk in r (FindJunk) and removes it through files;
// with files nil it only finds it. Junk keys hide the rest from restic,
// so once they're removed it looks again. Each file found says whether
// it was removed; the error is why the rest weren't.
func CleanJunk(ctx context.Context, r *Repo, files Files, good map[string]bool) ([]Junk, error) {
	batch, err := FindJunk(ctx, r, files, good)
	if err != nil || len(batch) == 0 || files == nil {
		return batch, err
	}
	var all []Junk
	for pass := 1; ; pass++ {
		keys := false
		for _, j := range batch {
			if err == nil {
				if err = files.Remove(ctx, j.Kind, j.Name); err != nil {
					err = fmt.Errorf("removing %s: %w", j.Path(), err)
				}
				j.Removed = err == nil
			}
			keys = keys || j.Kind == "keys"
			all = append(all, j)
		}
		// Twice at most: keys that come back between passes wait for the
		// next check.
		if err != nil || !keys || pass == 2 {
			return all, err
		}
		if batch, err = FindJunk(ctx, r, files, good); err != nil || len(batch) == 0 {
			return all, err
		}
	}
}

// LooksLikeJunk reports whether a restic error may come from junk in the
// repository (FindJunk): a file failing the authentication check, or a
// key file restic couldn't parse.
func LooksLikeJunk(msg string) bool {
	return strings.Contains(msg, junkError) || strings.Contains(msg, "Unmarshal") || strings.Contains(msg, "crypto.KDF")
}

func markGood(good map[string]bool, kind, name string) {
	if good != nil {
		good[kind+"/"+name] = true
	}
}

func sizeOf(sizes map[string]int64, kind, name string) int64 {
	if n, ok := sizes[kind+"/"+name]; ok {
		return n
	}
	return -1
}
