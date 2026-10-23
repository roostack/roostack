package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/s3api"
)

// Whether an S3 bucket keeps what a server deletes (SPEC.md §10.10).
// Other than on Backblaze, every server backing up to an account has the
// account's key, so its backups survive the server being taken over only
// when the bucket keeps old versions, the key can't erase them, and the
// bucket's lifecycle rule keeps them long enough. Roostack reads the
// first and last, and shows the second with a probe: it writes a small
// file, deletes it, and tries to erase the old version.

// S3Check is what checking an online storage account found.
type S3Check struct {
	// Versioned: the bucket keeps old versions of what's overwritten or
	// deleted.
	Versioned bool `json:"versioned"`
	// CanErase: the account's key erased the probe's old version.
	CanErase bool `json:"can_erase,omitempty"`
	// KeepsOld is how many days old versions are kept: 0 for good, -1
	// not known.
	KeepsOld int       `json:"keeps_old"`
	Checked  time.Time `json:"checked"`
	// Err says why the check couldn't finish.
	Err string `json:"error,omitempty"`
}

// HidesOnly reports whether what the key deletes is only hidden: the
// bucket keeps old versions and the key can't erase them.
func (c S3Check) HidesOnly() bool { return c.Err == "" && c.Versioned && !c.CanErase }

// s3CheckAge is how long a check holds: the weekly prune checks again.
// A check that failed is tried again after s3CheckRetry.
var (
	s3CheckAge   = 8 * 24 * time.Hour
	s3CheckRetry = time.Hour
)

// s3CheckMu keeps two checks of the file (and the bucket) apart.
var s3CheckMu sync.Mutex

// s3ChecksFile keeps the checks, by account (s3CheckKey).
func (m *Manager) s3ChecksFile() string { return filepath.Join(m.Lab.Paths.StateDir, "s3-checks.json") }

// s3CheckKey names an account and key in the file: a new key is checked
// again.
func s3CheckKey(s3 S3Provider) string {
	return s3.Endpoint + "/" + s3.Bucket + "/" + s3.Prefix + " " + s3.AccessKey
}

// s3Client is an S3 client for an account.
func (m *Manager) s3Client(s3 S3Provider) *s3api.Client {
	if m.NewS3 != nil {
		return m.NewS3(s3)
	}
	return &s3api.Client{Endpoint: s3.Endpoint, Bucket: s3.Bucket, AccessKey: s3.AccessKey, SecretKey: s3.SecretKey,
		Region: s3.Region()}
}

// s3Checks reads the saved checks.
func (m *Manager) s3Checks() map[string]S3Check {
	checks := map[string]S3Check{}
	if data, err := os.ReadFile(m.s3ChecksFile()); err == nil {
		json.Unmarshal(data, &checks)
	}
	return checks
}

// savedS3Check is an account's last check, if it still holds.
func (m *Manager) savedS3Check(s3 S3Provider) (S3Check, bool) {
	c, ok := m.s3Checks()[s3CheckKey(s3)]
	age := s3CheckAge
	if c.Err != "" {
		age = s3CheckRetry
	}
	return c, ok && time.Since(c.Checked) < age
}

// s3CheckOf is an account's check: the saved one, or a new one.
func (m *Manager) s3CheckOf(ctx context.Context, s3 S3Provider) S3Check {
	if c, ok := m.savedS3Check(s3); ok {
		return c
	}
	return m.checkS3(ctx, s3)
}

// checkS3 checks an account now, and saves what it found.
func (m *Manager) checkS3(ctx context.Context, s3 S3Provider) S3Check {
	asked := time.Now()
	s3CheckMu.Lock()
	defer s3CheckMu.Unlock()
	if c, ok := m.s3Checks()[s3CheckKey(s3)]; ok && c.Checked.After(asked) {
		return c // checked while this waited
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := m.probeS3(ctx, s3)
	c.Checked = time.Now()
	if c.Err != "" {
		m.Log.Warn("checking whether a bucket keeps old versions", "bucket", s3.Bucket, "err", c.Err)
	}
	checks := m.s3Checks()
	checks[s3CheckKey(s3)] = c
	if data, err := json.MarshalIndent(checks, "", "  "); err == nil {
		if err := fsutil.WritePrivate(m.s3ChecksFile(), data); err != nil {
			m.Log.Warn("saving a bucket's check", "err", err)
		}
	}
	return c
}

// probeS3 checks the bucket keeps versions, then writes a small file in
// the lab's folder, deletes it (which only hides it if versions are
// kept) and tries to erase its old version, which a safe key can't. The
// file's versions stay until the bucket's lifecycle rule erases them, as
// a server's would.
func (m *Manager) probeS3(ctx context.Context, s3 S3Provider) S3Check {
	cl := m.s3Client(s3)
	c := S3Check{KeepsOld: -1}
	status, err := cl.Versioning(ctx)
	if err != nil {
		c.Err = "reading whether the bucket keeps old versions: " + err.Error()
		return c
	}
	if status != "Enabled" {
		return c
	}
	labID, err := m.labID(ctx)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	folder := backup.ServerFolder(s3.Prefix, labID, "") + "/"
	probe := folder + "roostack-check"
	version, err := cl.Put(ctx, probe, []byte("Roostack is checking this bucket keeps old versions its key can't erase, and it will delete this file again.\n"))
	switch {
	case err != nil:
		c.Err = "writing a file to check with: " + err.Error()
		return c
	case version == "":
		c.Err = "the bucket says it keeps versions, but gave the file none"
		return c
	}
	c.Versioned = true
	if err := cl.Delete(ctx, probe, ""); err != nil {
		c.Err = "deleting the file it wrote: " + err.Error()
		return c
	}
	switch err := cl.Delete(ctx, probe, version); {
	case err == nil:
		c.CanErase = true
	case !errors.Is(err, s3api.ErrAccessDenied):
		c.Err = "trying to erase the file's old version: " + err.Error()
		return c
	}
	if days, err := cl.KeepsOld(ctx, folder); err == nil {
		c.KeepsOld = days
	} else {
		m.Log.Warn("reading a bucket's lifecycle rules", "bucket", s3.Bucket, "err", err)
	}
	return c
}

// sayS3Check tells a log what a check found: whether servers backing up
// to the account could destroy their backups there.
func sayS3Check(act *activity.Log, c S3Check) {
	switch {
	case c.Err != "":
		act.Printf("Roostack couldn't check whether this bucket keeps what's deleted (%s), so backups there will count as ones a server could delete.", c.Err)
	case !c.Versioned:
		act.Printf("Every server backing up here uses this account's key, which can delete its backups: if a server were taken over, they could be deleted too. " +
			"To protect them, turn on versioning for the bucket, with a rule that keeps old versions for 30 days, and give Roostack a key that can't delete old versions.")
	case c.CanErase:
		act.Printf("The bucket keeps old versions, but this account's key can erase them, so a server that's taken over could still destroy its backups here. " +
			"Give Roostack a key that can't delete old versions (without s3:DeleteObjectVersion).")
	default:
		act.Printf("The bucket keeps old versions and this account's key can't erase them: whatever a server deletes here is only hidden.")
		switch d := c.KeepsOld; {
		case d < 0:
			act.Printf("Roostack couldn't read how long the bucket keeps old versions.")
		case d == 0:
			act.Printf("Old versions are kept for good. Old backups the controller removes will still take space until a lifecycle rule erases them.")
		case d < MinKeepHidden:
			act.Printf("The bucket erases old versions after %s, so a server that's taken over could still destroy its backups. "+
				"In the bucket's lifecycle rules, keep old versions for at least %d days.", days(d), MinKeepHidden)
		default:
			act.Printf("Old versions are kept for %s.", days(d))
		}
	}
}

// days says a number of days.
func days(n int) string {
	if n == 1 {
		return "a day"
	}
	return fmt.Sprintf("%d days", n)
}
