// Package labrestore rebuilds a lab on a new server from its backups
// (SPEC.md §6.9, "Recoverable by default"): install Roostack, point it at the
// backups, enter the recovery key.
package labrestore

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/awssig"
	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/cloudkeys"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
)

// Source is where a lab's backups are.
type Source struct {
	// Kind is "folder" (a disk mounted on this server), "s3" (an
	// S3-compatible provider), "cloud" (Roostack Cloud backup, opened
	// with the recovery key alone) or "url" (any restic repository
	// address).
	Kind   string
	Folder string
	S3     backup.S3
	URL    string
}

// Problem is what's missing, for people, or "".
func (s Source) Problem() string {
	switch s.Kind {
	case "folder":
		if !filepath.IsAbs(strings.TrimSpace(s.Folder)) {
			return "Enter the folder's full path, e.g. /mnt/backup."
		}
	case "s3":
		return s.S3.Clean().Problem()
	case "url":
		if strings.TrimSpace(s.URL) == "" {
			return "Enter the backup's address, e.g. rest:https://…"
		}
	case "cloud":
	default:
		return "Choose where the backups are."
	}
	return ""
}

// Found is one server's backups that the recovery key opens.
type Found struct {
	// Repository and Env open it (Env holds credentials: never shown).
	Repository string
	Env        []string
	Snapshot   backup.Snapshot
	Snapshots  int
	// From the backup: the lab's name and domain, the server that made it
	// and its part in the lab.
	Name       string
	Domain     string
	ServerID   string
	ServerName string
	Controller bool
	// StateDir is where the server kept Roostack's state.
	StateDir string
	// Folder is the server whose folder the repository is in (FolderServer),
	// or "" if its address names none.
	Folder string
	// Lab is the lab in Roostack Cloud the backup belongs to (nil when it
	// isn't from Roostack Cloud).
	Lab *CloudLab
}

// Unopened is a backup the recovery key doesn't open (another lab's, or a
// mistyped key), that couldn't be read, or that isn't offered.
type Unopened struct {
	Repository string
	Problem    string
	Lab        *CloudLab
	// Forged: it says it's the controller's backup, but it isn't in that
	// server's folder (security review of 2026-10-16, M26). Pages show
	// it as a warning, not among the others.
	Forged bool
}

// CloudLab is a lab in Roostack Cloud with the recovery key. A lab that
// was unlinked and linked again has two, each with its own backups, so
// the person restoring chooses (SPEC.md §6.9).
type CloudLab struct {
	ID, Name string
	// LinkedAt is when it was linked; UnlinkedAt when it was unlinked (zero
	// while it's linked).
	LinkedAt, UnlinkedAt time.Time
}

// place is a repository to look in, with the credentials it needs.
type place struct {
	url string
	env []string
	lab *CloudLab
}

// Finder looks for a lab's backups.
type Finder struct {
	// OpenRepo opens a repository with the recovery key.
	OpenRepo func(url string, env []string) *backup.Repo
	HTTP     *http.Client
	// CloudAccess asks Roostack Cloud for the lab's backups there, proving
	// the recovery key (nil: Roostack Cloud is turned off).
	CloudAccess func(ctx context.Context) (cloudapi.RestoreAccess, error)
}

// maxRepos bounds a search (a bucket could hold many labs).
const maxRepos = 50

// Find lists the backups at src that the recovery key opens, newest
// first (from Roostack Cloud: each lab's, in Cloud's order), and those it
// doesn't, or that aren't offered.
func (f *Finder) Find(ctx context.Context, src Source) ([]Found, []Unopened, error) {
	places, empty, err := f.places(ctx, src)
	if err != nil {
		return nil, nil, err
	}
	if len(places) == 0 {
		if src.Kind == "cloud" {
			return nil, nil, errors.New("Roostack Cloud has no backups (any more) of a lab with this recovery key")
		}
		return nil, nil, errors.New("no Roostack backups there")
	}
	found := make([]*Found, len(places))
	errs := make([]error, len(places))
	var wg sync.WaitGroup
	for i, p := range places {
		wg.Go(func() {
			found[i], errs[i] = f.open(ctx, p)
		})
	}
	wg.Wait()
	var out []Found
	var unopened []Unopened
	for i, p := range places {
		if found[i] != nil {
			out = append(out, *found[i])
		} else {
			var forged *forgedError
			unopened = append(unopened, Unopened{Repository: p.url, Problem: describe(errs[i]), Lab: p.lab, Forged: errors.As(errs[i], &forged)})
		}
	}
	// Cloud lists its labs in order (the linked one, then the latest
	// unlinked); within a lab, the newest backup first.
	order := map[*CloudLab]int{}
	for _, p := range places {
		if _, ok := order[p.lab]; !ok {
			order[p.lab] = len(order)
		}
	}
	slices.SortFunc(out, func(a, b Found) int {
		return cmp.Or(cmp.Compare(order[a.Lab], order[b.Lab]), b.Snapshot.Time.Compare(a.Snapshot.Time))
	})
	for _, lab := range empty {
		unopened = append(unopened, Unopened{Lab: lab, Problem: "Roostack Cloud no longer keeps its backups."})
	}
	return out, unopened, nil
}

// forgedError is a backup that says it's the controller's, in another
// server's folder.
type forgedError struct{ name, id, folder string }

func (e *forgedError) Error() string {
	where := "a folder that isn't named after any server"
	if e.folder != "" {
		where = "server " + e.folder + "'s folder"
	}
	return fmt.Sprintf("It says it's the backup of %s (%s), the lab's controller, but it's in %s. Roostack keeps each server's backups "+
		"in a folder named after it, so this isn't offered: another server of the lab could have made it, to take the lab over when "+
		"it's restored. If you moved the backups yourself, put them back in a folder named %s.", e.name, e.id, where, e.id)
}

// FolderServer is the server whose folder a repository is in, from its
// address: every server's backups are in a folder named after its ID
// (<folder>/roostack/<lab>/<server> on a disk or online storage, Roostack
// Cloud's labs/<lab>/<server>, a friend's lab's <user>/<server>), except
// on a backup store of the lab's own, which keeps <server>/<lab>. A
// server can only write in its own folder there, so the folder before the
// last wins when it's a server's. "" if the address names no server.
func FolderServer(repository string) string {
	parts := strings.Split(strings.TrimRight(repository, "/"), "/")
	if n := len(parts); n >= 2 && nodeid.ValidID(parts[n-2]) {
		return parts[n-2]
	}
	if last := parts[len(parts)-1]; nodeid.ValidID(last) {
		return last
	}
	return ""
}

func describe(err error) string {
	switch {
	case errors.Is(err, backup.ErrWrongKey):
		return "The recovery key doesn't open it (another lab's backups, or a mistyped key)."
	case errors.Is(err, backup.ErrNoRepository):
		return "No backup there."
	case strings.Contains(err.Error(), "permission denied"):
		return "Roostack isn't allowed to read it: its files belong to another user (e.g. Roostack on the old server). " +
			"Let it read them with sudo chown -R roostack:roostack on the folder."
	}
	return err.Error()
}

// open reads a repository's latest Roostack backup and what it says about
// the server that made it.
func (f *Finder) open(ctx context.Context, p place) (*Found, error) {
	url, env := p.url, p.env
	repo := f.OpenRepo(url, env)
	snaps, err := repo.Snapshots(ctx, backup.TagRoostack, backup.TagServer)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, errors.New("It has no backups yet.")
	}
	slices.SortFunc(snaps, func(a, b backup.Snapshot) int { return a.Time.Compare(b.Time) })
	snap := snaps[len(snaps)-1]
	fd := &Found{Repository: url, Env: env, Snapshot: snap, Snapshots: len(snaps), Folder: FolderServer(url), Lab: p.lab}
	// The state folder is the backed-up path holding the server's identity.
	dirs := slices.Clone(snap.Paths)
	slices.SortStableFunc(dirs, func(a, b string) int { // the default first
		if a == config.DefaultStateDir {
			return -1
		}
		if b == config.DefaultStateDir {
			return 1
		}
		return 0
	})
	for _, dir := range dirs {
		data, err := repo.DumpFile(ctx, snap.ID, filepath.Join(dir, filepath.Base(nodeid.File(""))))
		if err != nil {
			continue
		}
		var id nodeid.Identity
		if json.Unmarshal(data, &id) != nil || id.ID == "" {
			continue
		}
		fd.StateDir, fd.ServerID, fd.ServerName = dir, id.ID, id.Name
		break
	}
	if fd.StateDir == "" {
		return nil, errors.New("It isn't a backup of a whole Roostack server.")
	}
	// A server that never joined a network is a lab of its own.
	fd.Controller = true
	if data, err := repo.DumpFile(ctx, snap.ID, labnet.MembershipFile(fd.StateDir)); err == nil {
		var m labnet.Membership
		if json.Unmarshal(data, &m) == nil {
			fd.Controller = m.Role != config.RoleNode
		}
	}
	// A server's backups hold whatever it puts in them, and each server
	// has the keys to its own folder: a node could make a backup that
	// says it's the controller, which a restore would then trust
	// (security review of 2026-10-16, M26). Only the controller's own
	// folder can hold the controller's backups.
	if fd.Controller && fd.Folder != fd.ServerID {
		return nil, &forgedError{name: cmp.Or(fd.ServerName, fd.ServerID), id: fd.ServerID, folder: fd.Folder}
	}
	if data, err := repo.DumpFile(ctx, snap.ID, filepath.Join(fd.StateDir, "lab.yaml")); err == nil {
		var lab struct {
			Name   string `yaml:"name"`
			Domain string `yaml:"domain"`
		}
		if yaml.Unmarshal(data, &lab) == nil {
			fd.Name, fd.Domain = lab.Name, lab.Domain
		}
	}
	return fd, nil
}

// places lists the restic repositories at src, with the credentials they
// need. From Roostack Cloud they're every lab's with the recovery key;
// empty are its labs with no backups kept.
func (f *Finder) places(ctx context.Context, src Source) (out []place, empty []*CloudLab, err error) {
	each := func(repos, env []string, lab *CloudLab) {
		for _, r := range repos {
			out = append(out, place{url: r, env: env, lab: lab})
		}
	}
	switch src.Kind {
	case "folder":
		folder := strings.TrimSpace(src.Folder)
		repos, denied, err := folderRepos(folder)
		if err == nil && len(repos) == 0 && len(denied) > 0 {
			err = unreadable(folder)
		}
		each(repos, nil, nil)
		return out, nil, err
	case "s3":
		s3 := src.S3.Clean()
		repos, err := f.s3Repos(ctx, s3)
		each(repos, s3.Env(), nil)
		return out, nil, err
	case "url":
		each([]string{strings.TrimSpace(src.URL)}, nil, nil)
		return out, nil, nil
	case "cloud":
		if f.CloudAccess == nil {
			return nil, nil, errors.New("Roostack Cloud is turned off on this server (cloud: disabled in lab.yaml)")
		}
		acc, err := f.CloudAccess(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range acc.Labs {
			lab := &CloudLab{ID: l.LabID, Name: l.LabName, LinkedAt: l.LinkedAt, UnlinkedAt: l.UnlinkedAt}
			if len(l.Repositories) == 0 {
				empty = append(empty, lab)
			}
			each(l.Repositories, cloudkeys.Env(l.Keys), lab)
			if len(out) >= maxRepos {
				out = out[:maxRepos]
				break
			}
		}
		return out, empty, nil
	}
	return nil, nil, fmt.Errorf("unknown kind of place %q", src.Kind)
}

// isRepo reports whether dir looks like a restic repository.
func isRepo(dir string) bool {
	for _, name := range []string{"config", "keys", "snapshots"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// folderRepos finds repositories in a folder and a few levels below it
// (e.g. <folder>/roostack/<lab>/<server>, or a backup store's
// <folder>/<server>/<lab>).
// It also lists the folders it wasn't allowed into.
func folderRepos(root string) (repos, denied []string, err error) {
	if _, err := os.Stat(root); err != nil {
		return nil, nil, fmt.Errorf("can't open %s: %w", root, err)
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrPermission) {
			denied = append(denied, path)
			return nil
		}
		if err != nil || !d.IsDir() {
			return nil // unreadable parts are skipped
		}
		if depth := strings.Count(strings.TrimPrefix(path, root), string(filepath.Separator)); depth > 5 {
			return filepath.SkipDir
		}
		if isRepo(path) {
			out = append(out, path)
			if len(out) >= maxRepos {
				return filepath.SkipAll
			}
			return filepath.SkipDir
		}
		return nil
	})
	return out, denied, err
}

// unreadable explains backups Roostack isn't allowed to read: typically a
// disk from the old server, whose files belong to its roostack user.
func unreadable(folder string) error {
	return fmt.Errorf("Roostack isn't allowed to read the files in %s: they belong to another user (e.g. Roostack on the old server). "+
		"Let it read them (sudo chown -R roostack:roostack %s), then look again", folder, folder)
}

// s3Repos finds repositories in a bucket: at the folder itself, and each
// server's under <folder>/roostack/<lab>/<server> (backup.ServerFolder).
func (f *Finder) s3Repos(ctx context.Context, s3 backup.S3) ([]string, error) {
	base := s3.Prefix
	if base != "" {
		base += "/"
	}
	url := func(prefix string) string {
		return "s3:" + s3.Endpoint + "/" + s3.Bucket + "/" + strings.TrimSuffix(prefix, "/")
	}
	var out []string
	if ok, err := f.s3Exists(ctx, s3, base+"config"); err != nil {
		return nil, err
	} else if ok {
		out = append(out, url(base))
	}
	labs, err := f.s3Folders(ctx, s3, base+"roostack/")
	if err != nil {
		return nil, err
	}
	for _, lab := range labs {
		servers, err := f.s3Folders(ctx, s3, lab)
		if err != nil {
			return nil, err
		}
		for _, srv := range servers {
			if len(out) < maxRepos {
				out = append(out, url(srv))
			}
		}
	}
	return out, nil
}

// s3Exists reports whether an object exists.
func (f *Finder) s3Exists(ctx context.Context, s3 backup.S3, key string) (bool, error) {
	var res listResult
	if err := f.s3List(ctx, s3, url.Values{"prefix": {key}, "max-keys": {"1"}}, &res); err != nil {
		return false, err
	}
	return len(res.Contents) > 0 && res.Contents[0].Key == key, nil
}

// s3Folders lists the "folders" directly under prefix.
func (f *Finder) s3Folders(ctx context.Context, s3 backup.S3, prefix string) ([]string, error) {
	var res listResult
	if err := f.s3List(ctx, s3, url.Values{"prefix": {prefix}, "delimiter": {"/"}, "max-keys": {"1000"}}, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range res.CommonPrefixes {
		out = append(out, p.Prefix)
	}
	return out, nil
}

type listResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// s3List lists a bucket (ListObjectsV2, path-style, signed).
func (f *Finder) s3List(ctx context.Context, s3 backup.S3, q url.Values, out *listResult) error {
	q.Set("list-type", "2")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s3.Endpoint+"/"+s3.Bucket+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Content-Sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") // empty body
	awssig.Sign(req, nil, s3.AccessKey, s3.SecretKey, s3.Region(), "s3", time.Now())
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", s3.Endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return errors.New("the provider refused the access key (check it, and that it may list the bucket)")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("there's no bucket called %q there", s3.Bucket)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("listing the bucket: %s", resp.Status)
	}
	return xml.Unmarshal(data, out)
}

// Shown is a repository address without any password in it, for pages.
func Shown(repository string) string {
	scheme, rest, ok := strings.Cut(repository, ":")
	if !ok {
		return repository
	}
	u, err := url.Parse(rest)
	if err != nil || u.User == nil {
		return repository
	}
	u.User = url.User(u.User.Username())
	return scheme + ":" + u.String()
}
