package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// Backups across servers (SPEC.md §10.10): each server chooses where it
// backs up, and the controller creates the repository there with the
// recovery key, adds a key for the server and hands over only that key.

// BackupStore is the controller's record of providers and targets.
type BackupStore interface {
	BackupProviders(ctx context.Context) ([]store.BackupProvider, error)
	AddBackupProvider(ctx context.Context, p store.BackupProvider) (int64, error)
	UpdateBackupProvider(ctx context.Context, p store.BackupProvider) error
	BackupTargets(ctx context.Context) ([]store.BackupTarget, error)
	SetBackupCopy(ctx context.Context, t store.BackupTarget) error
	BackupCopies(ctx context.Context) ([]store.BackupTarget, error)
	RemoveBackupCopy(ctx context.Context, serverID, provider string) error
	SetBackupTarget(ctx context.Context, t store.BackupTarget) error
	StoreAdmin(ctx context.Context, serverID string) (password, hash string, ok bool, err error)
	SetStoreAdmin(ctx context.Context, serverID, password, hash string) error
	LabID(ctx context.Context) (string, error)
}

// BackupChoice is a place a server can back up to.
type BackupChoice struct {
	// Ref is "server:<id>" (a server's disk) or "provider:<id>".
	Ref   string
	Title string
	// Note warns about the choice, e.g. shared credentials.
	Note string
}

// S3Provider is an S3-compatible provider.
type S3Provider = backup.S3

// storeReady is how long the controller waits for a server's backup
// store to start (its first start downloads rest-server).
var storeReady = 5 * time.Minute

// BackupChoices lists where a server can back up: any server's disk
// (over Roostack's network), and each provider.
func (m *Manager) BackupChoices(ctx context.Context) ([]BackupChoice, error) {
	var out []BackupChoice
	if m.Registry != nil { // the lab has a network
		servers, err := m.Servers()
		if err != nil {
			return nil, err
		}
		for _, mc := range servers {
			out = append(out, BackupChoice{Ref: "server:" + mc.ID, Title: mc.Name + "'s disk",
				Note: "Kept in " + mc.Name + "'s backups folder. A copy somewhere else protects against theft, fire or a failed disk."})
		}
	}
	if m.BackupStore == nil {
		return out, nil
	}
	providers, err := m.BackupStore.BackupProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		note := "These credentials can reach the whole bucket; each server uses its own folder."
		switch p.Kind {
		case "remote":
			var r RemoteStorage
			json.Unmarshal([]byte(p.Config), &r)
			note = fmt.Sprintf("Encrypted before it leaves; %s can't read it. Up to %s for this whole lab.", r.HostName, sizeGB(r.Quota))
		case "s3":
			var s3 S3Provider
			if json.Unmarshal([]byte(p.Config), &s3) == nil && b2.IsB2(s3.Endpoint) {
				note = "Each server gets its own key, limited to its folder (if this account's key may create keys)."
			}
		}
		out = append(out, BackupChoice{Ref: fmt.Sprintf("provider:%d", p.ID), Title: p.Name, Note: note})
	}
	return out, nil
}

// AddS3Provider records an S3-compatible provider any server can use.
func (m *Manager) AddS3Provider(ctx context.Context, name string, p S3Provider) error {
	p = p.Clean()
	name = strings.TrimSpace(name)
	if name == "" {
		return formErrorf("Give it a name, such as \"Backblaze\".")
	}
	if msg := p.Problem(); msg != "" {
		return formErrorf("%s", msg)
	}
	cfg, _ := json.Marshal(p)
	_, err := m.BackupStore.AddBackupProvider(ctx, store.BackupProvider{Kind: "s3", Name: name, Config: string(cfg)})
	return err
}

// SetBackupDestination points a server's backups at a choice. The
// repository is created in the background (activity.BackupSetup); the
// server then restarts Roostack to start backing up there.
func (m *Manager) SetBackupDestination(ctx context.Context, serverID, ref string) error {
	if m.BackupStore == nil || m.OpenRepo == nil {
		return ErrNotFound
	}
	n := m.node(serverID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("Roostack can't reach %s right now.", n.Identity().Name)
	}
	if err := m.recoveryKey(); err != nil {
		return err
	}
	choice, err := m.choiceFor(ctx, serverID, ref)
	if err != nil {
		return err
	}
	if choice == nil {
		return formErrorf("Choose where backups go.")
	}
	name := n.Identity().Name
	act := m.Activity.Start(activity.BackupSetup, "Setting up backups for "+name)
	go func() {
		ctx := context.Background()
		err := m.setUpRepository(ctx, act, serverID, n, *choice)
		if err != nil {
			m.Log.Error("setting up backups", "server", name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

func (m *Manager) setUpRepository(ctx context.Context, act *activity.Log, serverID string, n node.Node, choice BackupChoice) error {
	target, dest, err := m.createRepository(ctx, act, serverID, choice, m.BackupStore.SetBackupTarget)
	if err != nil {
		return err
	}
	if err := m.BackupStore.SetBackupTarget(ctx, target); err != nil {
		return err
	}
	act.Step("Telling %s where to back up (it restarts Roostack)", m.nameOf(serverID))
	if err := n.Settings().SetBackupDestination(ctx, dest); err != nil {
		return err
	}
	act.Step("Backups for %s go to %s", m.nameOf(serverID), choice.Title)
	return nil
}

// createRepository creates a server's repository at a choice, with the
// recovery key and a key of the server's own, and returns its record and
// what the server needs to use it. record saves the record early for a
// server's disk, whose store must learn the new user before the
// repository can be created.
func (m *Manager) createRepository(ctx context.Context, act *activity.Log, serverID string, choice BackupChoice,
	record func(context.Context, store.BackupTarget) error) (store.BackupTarget, node.BackupDestination, error) {
	labID, err := m.labID(ctx)
	if err != nil {
		return store.BackupTarget{}, node.BackupDestination{}, err
	}
	target := store.BackupTarget{ServerID: serverID, Provider: choice.Ref, Created: time.Now()}
	fail := func(err error) (store.BackupTarget, node.BackupDestination, error) {
		return store.BackupTarget{}, node.BackupDestination{}, err
	}
	var admin *backup.Repo // the controller's access, with the recovery key
	var dest node.BackupDestination
	kind, id, _ := strings.Cut(choice.Ref, ":")
	switch kind {
	case "server":
		host := id
		addr := m.addressOf(host)
		if addr == "" {
			return fail(fmt.Errorf("no address for server %s", host))
		}
		password, hash, err := newPassword()
		if err != nil {
			return fail(err)
		}
		adminPW, err := m.storeAdmin(ctx, host)
		if err != nil {
			return fail(err)
		}
		path := "/" + serverID + "/" + labID
		target.Repository = fmt.Sprintf("rest:http://%s:%d%s", addr, generate.StoragePort, path)
		target.StoreHost, target.StoreHash = host, hash
		dest = node.BackupDestination{Repository: target.Repository,
			Env: []string{"RESTIC_REST_USERNAME=" + serverID, "RESTIC_REST_PASSWORD=" + password}}
		admin = m.OpenRepo(fmt.Sprintf("rest:http://%s:%d%s", addr, generate.StorageAdminPort, path),
			[]string{"RESTIC_REST_USERNAME=controller", "RESTIC_REST_PASSWORD=" + adminPW})
		// The storing server starts its store (or learns the new user).
		act.Step("Starting the backup store on %s", m.nameOf(host))
		if err := record(ctx, target); err != nil {
			return fail(err)
		}
		m.Apply()
	case "drive", "folder":
		// A drive (or folder) on this server, readied by roostack-agent; the
		// repository is a local folder on it.
		if serverID != m.Lab.NodeID || m.Drives == nil {
			return fail(fmt.Errorf("a drive can only be chosen for the server it's in"))
		}
		dir := id
		var drive config.BackupDrive
		if kind == "drive" {
			act.Step("Getting the drive ready (mounting it for good if need be)")
			used, err := m.Drives.UseDrive(ctx, id)
			if err != nil {
				return fail(err)
			}
			dir, drive = used.Dir, config.BackupDrive{Mount: used.Dir, UUID: used.UUID, Name: choice.Title}
		} else if _, err := m.Drives.PrepareBackupFolder(ctx, id); err != nil {
			return fail(err)
		}
		target.Repository = filepath.Join(dir, backup.ServerFolder("", labID, serverID))
		dest = node.BackupDestination{Repository: target.Repository, Drive: drive}
		admin = m.OpenRepo(target.Repository, nil)
	case "provider":
		p, err := m.provider(ctx, id)
		if err != nil {
			return fail(err)
		}
		if p.Kind == "remote" {
			var r RemoteStorage
			if err := json.Unmarshal([]byte(p.Config), &r); err != nil {
				return fail(err)
			}
			url, env, adminURL, adminEnv := m.remoteRepos(r, labID, serverID)
			target.Repository = url
			dest = node.BackupDestination{Repository: url, Env: env}
			admin = m.OpenRepo(adminURL, adminEnv)
			break
		}
		var s3 S3Provider
		if err := json.Unmarshal([]byte(p.Config), &s3); err != nil {
			return fail(err)
		}
		target.Repository = s3.Repository(labID, serverID)
		env := s3.Env()
		dest = node.BackupDestination{Repository: target.Repository, Env: env}
		// The controller keeps using the account's key; the server gets
		// one limited to its folder where the provider can make one.
		admin = m.OpenRepo(target.Repository, env)
		if b2.IsB2(s3.Endpoint) {
			act.Step("Making a Backblaze key for %s, limited to its folder", m.nameOf(serverID))
			folder := backup.ServerFolder(s3.Prefix, labID, serverID) + "/"
			key, err := m.b2(s3).CreateKey(ctx, "roostack-"+labID+"-"+serverID, s3.Bucket, folder)
			if err != nil {
				act.Printf("%v. %s uses the account's key instead.", err, m.nameOf(serverID))
			} else {
				target.ScopedKey = key.ID
				scoped := s3
				scoped.AccessKey, scoped.SecretKey = key.ID, key.Secret
				dest.Env = scoped.Env()
			}
		}
	default:
		return fail(fmt.Errorf("unknown backup destination %q", choice.Ref))
	}

	act.Step("Creating the backup repository")
	deadline := time.Now().Add(storeReady)
	for attempt := 1; ; attempt++ {
		ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := admin.Init(ictx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("creating the repository: %w", err))
		}
		act.Printf("Attempt %d: %v; trying again shortly", attempt, firstLine(err.Error()))
		time.Sleep(10 * time.Second)
	}
	// The controller's own backups use the recovery key itself; any other
	// server gets a key of its own.
	dest.PrunedByController = true
	if serverID != m.Lab.NodeID {
		key, _, err := newPassword()
		if err != nil {
			return fail(err)
		}
		act.Step("Adding a key for %s", m.nameOf(serverID))
		if err := admin.AddKey(ctx, key, serverID); err != nil {
			return fail(err)
		}
		dest.Key = key
	}
	return target, dest, nil
}

// b2 is a Backblaze client for an account.
func (m *Manager) b2(s3 S3Provider) *b2.Client {
	if m.NewB2 != nil {
		return m.NewB2(s3.AccessKey, s3.SecretKey)
	}
	return &b2.Client{KeyID: s3.AccessKey, Key: s3.SecretKey}
}

// forgetServerBackups deletes provider keys made for a server that left
// the lab. Its backups stay (the recovery key still reads them).
func (m *Manager) forgetServerBackups(ctx context.Context, serverID string) {
	if m.BackupStore == nil {
		return
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return
	}
	copies, _ := m.BackupStore.BackupCopies(ctx)
	for _, t := range append(targets, copies...) {
		if t.ServerID != serverID {
			continue
		}
		m.BackupStore.RemoveBackupCopy(ctx, t.ServerID, t.Provider)
		if t.ScopedKey == "" {
			continue
		}
		kind, id, _ := strings.Cut(t.Provider, ":")
		if kind != "provider" {
			continue
		}
		p, err := m.provider(ctx, id)
		if err != nil {
			continue
		}
		var s3 S3Provider
		if json.Unmarshal([]byte(p.Config), &s3) != nil {
			continue
		}
		if err := m.b2(s3).DeleteKey(ctx, t.ScopedKey); err != nil {
			m.Log.Warn("deleting a removed server's Backblaze key", "server", serverID, "err", err)
		}
	}
}

// storageFor is what a server's backup store must allow: the servers
// backing up to it and the controller's admin access. Nil when no server
// backs up there: the store doesn't run.
func (m *Manager) storageFor(ctx context.Context, id string) *node.Storage {
	if m.BackupStore == nil {
		return nil
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		m.Log.Error("reading backup targets", "err", err)
		return nil
	}
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		m.Log.Error("reading backup copies", "err", err)
		return nil
	}
	st := &node.Storage{Users: map[string]string{}}
	for _, t := range append(targets, copies...) {
		if t.StoreHost == id {
			st.Users[t.ServerID] = t.StoreHash
		}
	}
	// A store nobody backs up to any more stops (its backups stay on disk).
	if len(st.Users) == 0 {
		return nil
	}
	_, hash, ok, err := m.BackupStore.StoreAdmin(ctx, id)
	if err != nil || !ok {
		return nil
	}
	st.Admin = hash
	return st
}

// storeAdmin returns the controller's password for a server's store,
// creating it on first use.
func (m *Manager) storeAdmin(ctx context.Context, host string) (string, error) {
	pw, _, ok, err := m.BackupStore.StoreAdmin(ctx, host)
	if err != nil || ok {
		return pw, err
	}
	pw, hash, err := newPassword()
	if err != nil {
		return "", err
	}
	return pw, m.BackupStore.SetStoreAdmin(ctx, host, pw, hash)
}

// labID names the lab in repository folders.
func (m *Manager) labID(ctx context.Context) (string, error) { return m.BackupStore.LabID(ctx) }

func (m *Manager) provider(ctx context.Context, id string) (store.BackupProvider, error) {
	ps, err := m.BackupStore.BackupProviders(ctx)
	if err != nil {
		return store.BackupProvider{}, err
	}
	for _, p := range ps {
		if fmt.Sprint(p.ID) == id {
			return p, nil
		}
	}
	return store.BackupProvider{}, errors.New("no such provider")
}

// addressOf is a server's address on Roostack's network, or "".
func (m *Manager) addressOf(id string) string {
	if id == m.Lab.NodeID {
		if m.Lab.OverlayAddr != "" {
			return m.Lab.OverlayAddr
		}
		return labnet.ControllerAddr.String()
	}
	for _, r := range m.others() {
		if r.Identity().ID == id {
			return r.Address()
		}
	}
	return ""
}

func (m *Manager) nameOf(id string) string {
	if n := m.node(id); n != nil {
		return n.Identity().Name
	}
	return id
}

// newPassword returns a random password and its bcrypt hash.
func newPassword() (string, string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	pw := hex.EncodeToString(b)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return pw, string(hash), err
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// BackupTargetOf is where a server backs up, if the controller set it up
// (ok false: not yet, or set up before servers chose destinations).
func (m *Manager) BackupTargetOf(ctx context.Context, serverID string) (t store.BackupTarget, title string, ok bool) {
	if m.BackupStore == nil {
		return t, "", false
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return t, "", false
	}
	choices, _ := m.BackupChoices(ctx)
	for _, x := range targets {
		if x.ServerID != serverID {
			continue
		}
		title = x.Repository
		switch {
		case strings.HasPrefix(x.Provider, "local:"), x.Provider == "setup" && strings.HasPrefix(x.Repository, "/"):
			title = "A folder on " + m.nameOf(x.ServerID)
		case x.Provider == "setup":
			title = "Chosen during setup"
		}
		for _, c := range choices {
			if c.Ref == x.Provider {
				title = c.Title
			}
		}
		if t := m.driveTitle(ctx, serverID, x.Provider); t != "" {
			title = t
		}
		return x, title, true
	}
	return t, "", false
}

// PruneJob removes old backups from every repository the controller made
// (SPEC.md §10.10): servers may only add backups, so the controller does
// this with the recovery key, weekly.
func (m *Manager) PruneJob() maintenance.Job {
	return maintenance.Job{
		Name: "removing old backups of every server",
		Run: func(ctx context.Context, h *maintenance.Handle) (err error) {
			act := m.Activity.Start(activity.Backup, "Removing old backups of every server")
			defer func() { act.Finish(err) }()
			targets, err := m.BackupStore.BackupTargets(ctx)
			if err != nil {
				return err
			}
			copies, err := m.BackupStore.BackupCopies(ctx)
			if err != nil {
				return err
			}
			var errs []error
			for _, t := range append(targets, copies...) {
				if strings.HasPrefix(t.Provider, "setup") {
					continue // set up before this; that server prunes it itself
				}
				repo, err := m.adminRepo(ctx, t)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				act.Step("%s: keeping 7 daily, 4 weekly and 6 monthly", m.nameOf(t.ServerID))
				if err := repo.Forget(ctx, backup.DefaultRetention); err != nil {
					act.Printf("%v", err)
					errs = append(errs, fmt.Errorf("%s: %w", m.nameOf(t.ServerID), err))
				}
			}
			return errors.Join(errs...)
		},
	}
}

// adminRepo opens a server's repository as the controller: through the
// store's admin access for a server's disk, or the provider's
// credentials.
func (m *Manager) adminRepo(ctx context.Context, t store.BackupTarget) (*backup.Repo, error) {
	kind, id, _ := strings.Cut(t.Provider, ":")
	switch kind {
	case "local", "drive", "folder": // a drive or folder on the controller
		return m.OpenRepo(t.Repository, nil), nil
	case "server":
		pw, _, ok, err := m.BackupStore.StoreAdmin(ctx, t.StoreHost)
		if err != nil || !ok {
			return nil, fmt.Errorf("no admin access to %s's backup store", m.nameOf(t.StoreHost))
		}
		url := strings.Replace(t.Repository, fmt.Sprintf(":%d/", generate.StoragePort), fmt.Sprintf(":%d/", generate.StorageAdminPort), 1)
		return m.OpenRepo(url, []string{"RESTIC_REST_USERNAME=controller", "RESTIC_REST_PASSWORD=" + pw}), nil
	case "provider":
		p, err := m.provider(ctx, id)
		if err != nil {
			return nil, err
		}
		if p.Kind == "remote" {
			var r RemoteStorage
			if err := json.Unmarshal([]byte(p.Config), &r); err != nil {
				return nil, err
			}
			labID, err := m.labID(ctx)
			if err != nil {
				return nil, err
			}
			_, _, url, env := m.remoteRepos(r, labID, t.ServerID)
			return m.OpenRepo(url, env), nil
		}
		var s3 S3Provider
		if err := json.Unmarshal([]byte(p.Config), &s3); err != nil {
			return nil, err
		}
		return m.OpenRepo(t.Repository, s3.Env()), nil
	}
	return nil, fmt.Errorf("unknown backup destination %q", t.Provider)
}

// AdoptBackupDestination records a server's destination set up before
// destinations were recorded (e.g. by an older setup wizard), so its
// Backups page shows it. That server keeps pruning it itself.
func (m *Manager) AdoptBackupDestination(ctx context.Context, repository string) error {
	if m.BackupStore == nil || repository == "" {
		return nil
	}
	if _, _, ok := m.BackupTargetOf(ctx, m.Lab.NodeID); ok {
		return nil
	}
	return m.BackupStore.SetBackupTarget(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "setup",
		Repository: repository, Created: time.Now()})
}

// DriveAgent is roostack-agent's backup drive operations.
type DriveAgent interface {
	Drives(ctx context.Context) ([]agent.Drive, error)
	UseDrive(ctx context.Context, id string) (agent.UsedDrive, error)
	PrepareBackupFolder(ctx context.Context, dir string) (string, error)
}

// recoveryKey makes sure the lab has its recovery key: the first backup set
// up creates it.
func (m *Manager) recoveryKey() error {
	if m.HasRecoveryKey == nil || m.HasRecoveryKey() {
		return nil
	}
	if m.EnsureRecoveryKey != nil {
		return m.EnsureRecoveryKey()
	}
	return formErrorf("Set up this server's backups first: that creates the recovery key every backup can be read with.")
}

// choiceFor finds a place a server can back up: one of BackupChoices, or a
// drive ("drive:<id>") or folder ("folder:<path>") on this server.
func (m *Manager) choiceFor(ctx context.Context, serverID, ref string) (*BackupChoice, error) {
	kind, id, _ := strings.Cut(ref, ":")
	switch kind {
	case "drive":
		if serverID != m.Lab.NodeID || m.Drives == nil {
			return nil, formErrorf("A drive can only hold backups of the server it's in.")
		}
		drives, err := m.Drives.Drives(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range drives {
			if d.ID == id && d.Usable {
				return &BackupChoice{Ref: ref, Title: DriveTitle(d)}, nil
			}
		}
		return nil, formErrorf("That drive isn't connected any more.")
	case "folder":
		if serverID != m.Lab.NodeID || m.Drives == nil || !filepath.IsAbs(id) {
			return nil, formErrorf("Enter a full folder path, starting with /.")
		}
		return &BackupChoice{Ref: "folder:" + filepath.Clean(id), Title: "The folder " + filepath.Clean(id)}, nil
	}
	choices, err := m.BackupChoices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range choices {
		if choices[i].Ref == ref {
			return &choices[i], nil
		}
	}
	return nil, nil
}

// driveTitle names a drive or folder destination ("" for other kinds).
func (m *Manager) driveTitle(ctx context.Context, serverID, ref string) string {
	kind, id, _ := strings.Cut(ref, ":")
	switch kind {
	case "drive":
		if c, err := m.choiceFor(ctx, serverID, ref); err == nil && c != nil {
			return c.Title
		}
		return "A drive that isn't connected now"
	case "folder":
		return "The folder " + id
	}
	return ""
}

// DriveTitle names a drive for people ("USB drive BACKUP").
func DriveTitle(d agent.Drive) string {
	switch d.Kind {
	case "system":
		return "This server's own disk"
	case "usb":
		return "USB drive " + d.Name
	case "network":
		return "Network share " + d.Name
	}
	return "Disk " + d.Name
}
