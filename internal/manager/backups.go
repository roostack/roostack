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
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/store"
)

// Backups across machines (SPEC.md §10.10): each machine chooses where it
// backs up, and the controller creates the repository there with the
// recovery key, adds a key for the machine and hands over only that key.

// BackupStore is the controller's record of providers and targets.
type BackupStore interface {
	BackupProviders(ctx context.Context) ([]store.BackupProvider, error)
	AddBackupProvider(ctx context.Context, p store.BackupProvider) (int64, error)
	BackupTargets(ctx context.Context) ([]store.BackupTarget, error)
	SetBackupTarget(ctx context.Context, t store.BackupTarget) error
	StoreAdmin(ctx context.Context, machineID string) (password, hash string, ok bool, err error)
	SetStoreAdmin(ctx context.Context, machineID, password, hash string) error
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// BackupChoice is a place a machine can back up to.
type BackupChoice struct {
	// Ref is "machine:<id>" (a machine's disk) or "provider:<id>".
	Ref   string
	Title string
	// Note warns about the choice, e.g. shared credentials.
	Note string
}

// S3Provider is an S3-compatible provider (Backblaze B2, Wasabi, AWS, …).
type S3Provider struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// storeReady is how long the controller waits for a machine's backup
// store to start (its first start downloads rest-server).
var storeReady = 5 * time.Minute

// BackupChoices lists where a machine can back up: any machine's disk
// (over MyLab's network), and each provider.
func (m *Manager) BackupChoices(ctx context.Context) ([]BackupChoice, error) {
	var out []BackupChoice
	if m.Registry != nil { // the lab has a network
		machines, err := m.Machines()
		if err != nil {
			return nil, err
		}
		for _, mc := range machines {
			out = append(out, BackupChoice{Ref: "machine:" + mc.ID, Title: mc.Name + "'s disk",
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
		out = append(out, BackupChoice{Ref: fmt.Sprintf("provider:%d", p.ID), Title: p.Name,
			Note: "These credentials can reach the whole bucket; each machine uses its own folder."})
	}
	return out, nil
}

// AddS3Provider records an S3-compatible provider any machine can use.
func (m *Manager) AddS3Provider(ctx context.Context, name string, p S3Provider) error {
	p.Endpoint = strings.TrimRight(strings.TrimSpace(p.Endpoint), "/")
	p.Bucket = strings.Trim(strings.TrimSpace(p.Bucket), "/")
	p.Prefix = strings.Trim(strings.TrimSpace(p.Prefix), "/")
	p.AccessKey, p.SecretKey = strings.TrimSpace(p.AccessKey), strings.TrimSpace(p.SecretKey)
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return formErrorf("Give it a name, such as \"Backblaze\".")
	case !strings.HasPrefix(p.Endpoint, "https://") || p.Bucket == "":
		return formErrorf("Enter the storage endpoint (starting with https://) and a bucket name.")
	case p.AccessKey == "" || p.SecretKey == "":
		return formErrorf("Enter the access key and secret key.")
	}
	cfg, _ := json.Marshal(p)
	_, err := m.BackupStore.AddBackupProvider(ctx, store.BackupProvider{Kind: "s3", Name: name, Config: string(cfg)})
	return err
}

// SetBackupDestination points a machine's backups at a choice. The
// repository is created in the background (activity.BackupSetup); the
// machine then restarts MyLab to start backing up there.
func (m *Manager) SetBackupDestination(ctx context.Context, machineID, ref string) error {
	if m.BackupStore == nil || m.OpenRepo == nil {
		return ErrNotFound
	}
	n := m.node(machineID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("MyLab can't reach %s right now.", n.Identity().Name)
	}
	if m.HasRecoveryKey != nil && !m.HasRecoveryKey() {
		return formErrorf("Set up this machine's backups first: that creates the recovery key every backup can be read with.")
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
		err := m.setUpRepository(ctx, act, machineID, n, *choice)
		if err != nil {
			m.Log.Error("setting up backups", "machine", name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

func (m *Manager) setUpRepository(ctx context.Context, act *activity.Log, machineID string, n node.Node, choice BackupChoice) error {
	labID, err := m.labID(ctx)
	if err != nil {
		return err
	}
	target := store.BackupTarget{MachineID: machineID, Provider: choice.Ref, Created: time.Now()}
	var admin *backup.Repo // the controller's access, with the recovery key
	var dest node.BackupDestination
	kind, id, _ := strings.Cut(choice.Ref, ":")
	switch kind {
	case "machine":
		host := id
		addr := m.addressOf(host)
		if addr == "" {
			return fmt.Errorf("no address for machine %s", host)
		}
		password, hash, err := newPassword()
		if err != nil {
			return err
		}
		adminPW, err := m.storeAdmin(ctx, host)
		if err != nil {
			return err
		}
		path := "/" + machineID + "/" + labID
		target.Repository = fmt.Sprintf("rest:http://%s:%d%s", addr, generate.StoragePort, path)
		target.StoreHost, target.StoreHash = host, hash
		dest = node.BackupDestination{Repository: target.Repository,
			Env: []string{"RESTIC_REST_USERNAME=" + machineID, "RESTIC_REST_PASSWORD=" + password}}
		admin = m.OpenRepo(fmt.Sprintf("rest:http://%s:%d%s", addr, generate.StorageAdminPort, path),
			[]string{"RESTIC_REST_USERNAME=controller", "RESTIC_REST_PASSWORD=" + adminPW})
		// The storing machine starts its store (or learns the new user).
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
		var s3 S3Provider
		if err := json.Unmarshal([]byte(p.Config), &s3); err != nil {
			return err
		}
		folder := strings.Trim(strings.Join([]string{s3.Prefix, "mylab", labID, machineID}, "/"), "/")
		target.Repository = "s3:" + s3.Endpoint + "/" + s3.Bucket + "/" + folder
		env := []string{"AWS_ACCESS_KEY_ID=" + s3.AccessKey, "AWS_SECRET_ACCESS_KEY=" + s3.SecretKey}
		dest = node.BackupDestination{Repository: target.Repository, Env: env}
		admin = m.OpenRepo(target.Repository, env)
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
	// machine gets a key of its own.
	if machineID != m.Lab.NodeID {
		key, _, err := newPassword()
		if err != nil {
			return err
		}
		act.Step("Adding a key for %s", m.nameOf(machineID))
		if err := admin.AddKey(ctx, key, machineID); err != nil {
			return err
		}
		dest.Key = key
	}
	if err := m.BackupStore.SetBackupTarget(ctx, target); err != nil {
		return err
	}
	act.Step("Telling %s where to back up (it restarts MyLab)", m.nameOf(machineID))
	if err := n.Settings().SetBackupDestination(ctx, dest); err != nil {
		return err
	}
	act.Step("Backups for %s go to %s", m.nameOf(machineID), choice.Title)
	return nil
}

// storageFor is what a machine's backup store must allow: the machines
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
			st.Users[t.MachineID] = t.StoreHash
		}
	}
	_, hash, ok, err := m.BackupStore.StoreAdmin(ctx, id)
	if err != nil || !ok {
		return nil
	}
	st.Admin = hash
	return st
}

// storeAdmin returns the controller's password for a machine's store,
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

// labID names the lab in repository folders; made once, never changed.
func (m *Manager) labID(ctx context.Context) (string, error) {
	id, err := m.BackupStore.Setting(ctx, "lab.id")
	if err != nil || id != "" {
		return id, err
	}
	b := make([]byte, 5)
	rand.Read(b)
	id = "lab-" + hex.EncodeToString(b)
	return id, m.BackupStore.SetSetting(ctx, "lab.id", id)
}

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

// addressOf is a machine's address on MyLab's network, or "".
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

// BackupTargetOf is where a machine backs up, if the controller set it up
// (ok false: not yet, or set up before machines chose destinations).
func (m *Manager) BackupTargetOf(ctx context.Context, machineID string) (t store.BackupTarget, title string, ok bool) {
	if m.BackupStore == nil {
		return t, "", false
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return t, "", false
	}
	choices, _ := m.BackupChoices(ctx)
	for _, x := range targets {
		if x.MachineID != machineID {
			continue
		}
		title = x.Repository
		for _, c := range choices {
			if c.Ref == x.Provider {
				title = c.Title
			}
		}
		return x, title, true
	}
	return t, "", false
}
