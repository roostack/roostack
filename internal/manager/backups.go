package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/b2"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/maintenance"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/store"
)

// Backups across servers (SPEC.md §10.10): each server chooses where it
// backs up, and the controller creates the repository there with the
// recovery key, adds a key for the server and hands over only that key.

// BackupStore is the controller's record of providers and targets.
type BackupStore interface {
	BackupProviders(ctx context.Context) ([]store.BackupProvider, error)
	AddBackupProvider(ctx context.Context, p store.BackupProvider) (int64, error)
	BackupTargets(ctx context.Context) ([]store.BackupTarget, error)
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
// (over MyLab's network), and each provider.
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
// server then restarts MyLab to start backing up there.
func (m *Manager) SetBackupDestination(ctx context.Context, serverID, ref string) error {
	if m.BackupStore == nil || m.OpenRepo == nil {
		return ErrNotFound
	}
	n := m.node(serverID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("MyLab can't reach %s right now.", n.Identity().Name)
	}
	if m.HasRecoveryKey != nil && !m.HasRecoveryKey() {
		return formErrorf("Set up this server's backups first: that creates the recovery key every backup can be read with.")
	}
	choices, err := m.BackupChoices(ctx)
	if err != nil {
		return err
	}
	var choice *BackupChoice
	for i := range choices {
		if choices[i].Ref == ref {
			choice = &choices[i]
		}
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
	labID, err := m.labID(ctx)
	if err != nil {
		return err
	}
	target := store.BackupTarget{ServerID: serverID, Provider: choice.Ref, Created: time.Now()}
	var admin *backup.Repo // the controller's access, with the recovery key
	var dest node.BackupDestination
	kind, id, _ := strings.Cut(choice.Ref, ":")
	switch kind {
	case "server":
		host := id
		addr := m.addressOf(host)
		if addr == "" {
			return fmt.Errorf("no address for server %s", host)
		}
		password, hash, err := newPassword()
		if err != nil {
			return err
		}
		adminPW, err := m.storeAdmin(ctx, host)
		if err != nil {
			return err
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
		if err := m.BackupStore.SetBackupTarget(ctx, target); err != nil {
			return err
		}
		m.Apply()
	case "provider":
		p, err := m.provider(ctx, id)
		if err != nil {
			return err
		}
		if p.Kind == "remote" {
			var r RemoteStorage
			if err := json.Unmarshal([]byte(p.Config), &r); err != nil {
				return err
			}
			url, env, adminURL, adminEnv := m.remoteRepos(r, labID, serverID)
			target.Repository = url
			dest = node.BackupDestination{Repository: url, Env: env}
			admin = m.OpenRepo(adminURL, adminEnv)
			break
		}
		var s3 S3Provider
		if err := json.Unmarshal([]byte(p.Config), &s3); err != nil {
			return err
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
			key, err := m.b2(s3).CreateKey(ctx, "mylab-"+labID+"-"+serverID, s3.Bucket, folder)
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
		return fmt.Errorf("unknown backup destination %q", choice.Ref)
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
			return fmt.Errorf("creating the repository: %w", err)
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
			return err
		}
		act.Step("Adding a key for %s", m.nameOf(serverID))
		if err := admin.AddKey(ctx, key, serverID); err != nil {
			return err
		}
		dest.Key = key
	}
	if err := m.BackupStore.SetBackupTarget(ctx, target); err != nil {
		return err
	}
	act.Step("Telling %s where to back up (it restarts MyLab)", m.nameOf(serverID))
	if err := n.Settings().SetBackupDestination(ctx, dest); err != nil {
		return err
	}
	act.Step("Backups for %s go to %s", m.nameOf(serverID), choice.Title)
	return nil
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
	for _, t := range targets {
		if t.ServerID != serverID || t.ScopedKey == "" {
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
// backing up to it and the controller's admin access.
func (m *Manager) storageFor(ctx context.Context, id string) *node.Storage {
	if m.BackupStore == nil {
		return nil
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		m.Log.Error("reading backup targets", "err", err)
		return nil
	}
	st := &node.Storage{Users: map[string]string{}}
	for _, t := range targets {
		if t.StoreHost == id {
			st.Users[t.ServerID] = t.StoreHash
		}
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

// addressOf is a server's address on MyLab's network, or "".
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
			var errs []error
			for _, t := range targets {
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
	case "local": // a folder on the controller, from the setup wizard
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
