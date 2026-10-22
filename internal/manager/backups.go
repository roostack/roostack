package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/cloudkeys"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
	"roostack.cloud/roostack/internal/usererr"
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
	RemoveBackupTarget(ctx context.Context, serverID string) error
	SetLostBackup(ctx context.Context, l store.LostBackup) error
	RemoveLostBackup(ctx context.Context, serverID string) error
	LostBackups(ctx context.Context) ([]store.LostBackup, error)
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

// CloudBackup is Roostack Cloud backup (Cloud's DESIGN §6(b)): keys that
// expire, for a server's folder or the lab's prune, and what the lab
// stores against its quota.
type CloudBackup interface {
	StorageKeys(ctx context.Context, req cloudapi.StorageRequest) (cloudapi.StorageKeys, error)
	StorageUsage(ctx context.Context) (cloudapi.StorageUsage, error)
	// RegisterIdentity tells Cloud the recovery key's identity (after a
	// new recovery key), with which the lab can be restored from Cloud.
	RegisterIdentity(ctx context.Context) error
}

// cloudChoice is Roostack Cloud backup as a choice, if this lab's account
// may use it (a storage grant, in the pilot).
func (m *Manager) cloudChoice(ctx context.Context) (BackupChoice, bool) {
	if m.CloudBackup == nil {
		return BackupChoice{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u, err := m.CloudBackup.StorageUsage(ctx)
	if err != nil || u.QuotaBytes <= 0 {
		return BackupChoice{}, false
	}
	used := fmt.Sprintf("This lab uses %s of %s.", units.Bytes(u.UsedBytes), units.Bytes(u.QuotaBytes))
	if u.AccountUsedBytes > u.UsedBytes {
		// The account's other labs share the space (a Cloud from before
		// 2026-10-19 doesn't say).
		used = fmt.Sprintf("Your account's labs use %s of %s; this lab %s.", units.Bytes(u.AccountUsedBytes),
			units.Bytes(u.QuotaBytes), units.Bytes(u.UsedBytes))
	}
	return BackupChoice{Ref: destination.Cloud, Title: "Roostack Cloud",
		Note: "Encrypted with your recovery key before it leaves; Roostack Cloud can't read it. " + used}, true
}

// cloudPruneKeys are the controller's keys to the lab's folders in
// Roostack Cloud backup (for creating repositories, checks and pruning),
// kept while fresh (state/cloud-prune-keys.json).
func (m *Manager) cloudPruneKeys() *cloudkeys.Cache {
	return &cloudkeys.Cache{Path: cloudkeys.File(m.Lab.Paths.StateDir, "prune"),
		Source: func(ctx context.Context) (cloudapi.StorageKeys, error) {
			if m.CloudBackup == nil {
				return cloudapi.StorageKeys{}, errors.New("the lab isn't linked to Roostack Cloud")
			}
			return m.CloudBackup.StorageKeys(ctx, cloudapi.StorageRequest{Purpose: cloudapi.StoragePrune})
		}}
}

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
			out = append(out, BackupChoice{Ref: destination.New(destination.Server, mc.ID), Title: mc.Name + "'s disk",
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
		ref := destination.OfProvider(p.ID)
		d, err := m.destinationOf(ctx, ref)
		if err != nil {
			continue
		}
		note := "Every server uses this account's key, which can delete what's there; each server uses its own folder."
		switch c, checked := m.savedS3Check(d.s3); {
		case d.kind == destS3 && !d.isB2() && checked && c.HidesOnly():
			note = "Every server uses this account's key, in its own folder; the bucket keeps what's deleted, so it can be brought back."
		case d.kind == destRemote:
			note = fmt.Sprintf("Encrypted before it leaves; %s can't read it. Up to %s for this whole lab.", d.remote.HostName, sizeGB(d.remote.Quota))
		case d.isB2():
			note = "Each server gets its own key, limited to its folder, that can add backups but not erase them (if this account's key may create keys)."
		}
		out = append(out, BackupChoice{Ref: ref, Title: p.Name, Note: note})
	}
	if c, ok := m.cloudChoice(ctx); ok {
		out = append(out, c)
	}
	return out, nil
}

// AddS3Provider records an S3-compatible provider any server can use.
func (m *Manager) AddS3Provider(ctx context.Context, name string, p S3Provider) error {
	p = p.Clean()
	name = strings.TrimSpace(name)
	if name == "" {
		return usererr.New("Give it a name, such as \"Backblaze\".")
	}
	if msg := p.Problem(); msg != "" {
		return usererr.New("%s", msg)
	}
	cfg, _ := json.Marshal(p)
	_, err := m.BackupStore.AddBackupProvider(ctx, store.BackupProvider{Kind: "s3", Name: name, Config: string(cfg)})
	return err
}

// SetBackupDestination points a server's backups at a choice. The
// repository is created in the background (activity.BackupSetupFor); the
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
		return usererr.New("Roostack can't reach %s right now.", n.Identity().Name)
	}
	if err := m.recoveryKey(); err != nil {
		return err
	}
	choice, err := m.choiceFor(ctx, serverID, ref)
	if err != nil {
		return err
	}
	if choice == nil {
		return usererr.New("Choose where backups go.")
	}
	name := n.Identity().Name
	title := "Setting up backups for " + name
	if m.alone() {
		title = "Setting up backups"
	}
	act := m.Activity.Start(activity.BackupSetupFor(serverID), title)
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

func (m *Manager) setUpRepository(ctx context.Context, act *activity.Log, serverID string, n node.Node, choice BackupChoice) (err error) {
	// The target is recorded early (a backup store learns its new user
	// from it); if setting up fails, the one before is put back.
	prev, _, hadPrev := m.BackupTargetOf(ctx, serverID)
	defer func() {
		if err == nil {
			return
		}
		undo := m.BackupStore.RemoveBackupTarget(ctx, serverID)
		if hadPrev {
			undo = m.BackupStore.SetBackupTarget(ctx, prev)
		}
		if undo != nil {
			m.Log.Error("putting back where backups went", "server", serverID, "err", undo)
		}
		m.Apply()
	}()
	target, dest, err := m.createRepository(ctx, act, serverID, choice, m.BackupStore.SetBackupTarget)
	if err != nil {
		return err
	}
	if err := m.BackupStore.SetBackupTarget(ctx, target); err != nil {
		return err
	}
	if m.alone() {
		act.Step("Saving where backups go (Roostack restarts to use it)")
	} else {
		act.Step("Telling %s where to back up (it restarts Roostack)", m.nameOf(serverID))
	}
	if err := n.Settings().SetBackupDestination(ctx, dest); err != nil {
		return err
	}
	if m.alone() {
		act.Step("Backups go to %s", inSentence(choice.Title))
	} else {
		act.Step("Backups for %s go to %s", m.nameOf(serverID), inSentence(choice.Title))
	}
	if err := m.BackupStore.RemoveLostBackup(ctx, serverID); err != nil {
		m.Log.Warn("forgetting a lost main backup", "server", serverID, "err", err)
	}
	return nil
}

// LostMainBackup says whether a server's main backup was on a server that
// left the lab, while it has no new one (SPEC.md §10.10).
func (m *Manager) LostMainBackup(ctx context.Context, serverID string) (store.LostBackup, bool) {
	if m.BackupStore == nil {
		return store.LostBackup{}, false
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil || slices.ContainsFunc(targets, func(t store.BackupTarget) bool { return t.ServerID == serverID }) {
		return store.LostBackup{}, false
	}
	lost, _ := m.BackupStore.LostBackups(ctx)
	for _, l := range lost {
		if l.ServerID == serverID {
			return l, true
		}
	}
	return store.LostBackup{}, false
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
	ref := destination.Parse(choice.Ref)
	kind, id := ref.Kind, ref.ID
	switch kind {
	case destination.Cloud:
		// Roostack Cloud backup: the server's folder in Cloud's bucket,
		// reached with keys that expire (fetched before each backup); the
		// controller creates and prunes it with the lab's prune keys.
		if m.CloudBackup == nil {
			return fail(errors.New("the lab isn't linked to Roostack Cloud"))
		}
		if m.alone() {
			act.Step("Asking Roostack Cloud for a folder for this server")
		} else {
			act.Step("Asking Roostack Cloud for %s's folder (one of %s's servers)", m.nameOf(serverID), m.Lab.DisplayName())
		}
		keys, err := m.CloudBackup.StorageKeys(ctx, cloudapi.StorageRequest{Purpose: cloudapi.StorageBackup, Server: serverID})
		if err != nil {
			return fail(err)
		}
		target.Repository = keys.Repository
		dest = node.BackupDestination{Repository: keys.Repository, Cloud: true}
		admin = m.OpenRepo(keys.Repository, nil)
		admin.Credentials = m.cloudPruneKeys().Env
	case destination.Server:
		host := id
		addr := m.addressOf(host)
		if addr == "" {
			return fail(fmt.Errorf("no address for server %s", host))
		}
		password, hash, err := labnet.NewPassword()
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
		act.Step("Starting the backup store%s", m.onServer(host))
		if err := record(ctx, target); err != nil {
			return fail(err)
		}
		m.Apply()
	case destination.Drive, destination.Folder:
		// A drive (or folder) on this server, readied by roostack-agent; the
		// repository is a local folder on it.
		if serverID != m.Lab.NodeID || m.Drives == nil {
			return fail(fmt.Errorf("a drive can only be chosen for the server it's in"))
		}
		dir := id
		var drive config.BackupDrive
		if kind == destination.Drive {
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
	case destination.Provider:
		d, err := m.destinationOf(ctx, choice.Ref)
		if err != nil {
			return fail(err)
		}
		if d.kind == destRemote {
			r, err := m.remoteServerUser(ctx, choice.Ref, serverID)
			if err != nil {
				return fail(err)
			}
			url, env, adminURL, adminEnv := m.remoteRepos(r, labID, serverID)
			target.Repository = url
			dest = node.BackupDestination{Repository: url, Env: env}
			admin = m.OpenRepo(adminURL, adminEnv)
			break
		}
		s3 := d.s3
		target.Repository = s3.Repository(labID, serverID)
		env := s3.Env()
		dest = node.BackupDestination{Repository: target.Repository, Env: env}
		// The controller keeps using the account's key; the server gets
		// one limited to its folder, which can't erase, where the
		// provider can make one (SPEC.md §10.10).
		admin = m.OpenRepo(target.Repository, env)
		if d.isB2() {
			m.scopedB2Key(ctx, act, serverID, labID, s3, &target, &dest)
		} else {
			// Whether what it deletes is kept (SPEC.md §10.10).
			act.Step("Checking whether the bucket keeps what's deleted")
			sayS3Check(act, m.checkS3(ctx, s3))
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
		key, _, err := labnet.NewPassword()
		if err != nil {
			return fail(err)
		}
		act.Step("Adding a key for %s", m.nameOf(serverID))
		id, err := admin.AddKey(ctx, key, serverID)
		if err != nil {
			return fail(err)
		}
		dest.Key, dest.KeyHint = key, id
	} else {
		dest.KeyHint = admin.KeyID(ctx)
	}
	return target, dest, nil
}

// scopedB2Key makes a server a Backblaze key limited to its folder, which
// can add backups but not erase them, and says how long the bucket keeps
// what it hides. Without one (the account's key can't make keys), the
// server keeps the account's key.
func (m *Manager) scopedB2Key(ctx context.Context, act *activity.Log, serverID, labID string, s3 S3Provider,
	target *store.BackupTarget, dest *node.BackupDestination) {
	name := m.serverName(serverID)
	act.Step("Making a Backblaze key for %s, limited to its folder", name)
	folder := backup.ServerFolder(s3.Prefix, labID, serverID) + "/"
	key, err := m.b2(s3).CreateKey(ctx, "roostack-"+labID+"-"+serverID, s3.Bucket, folder)
	if err != nil {
		act.Printf("%v. %s uses the account's key instead, which can delete its backups there.", err, capital(name))
		return
	}
	target.ScopedKey = key.ID
	scoped := s3
	scoped.AccessKey, scoped.SecretKey = key.ID, key.Secret
	dest.Env = scoped.Env()
	act.Printf("%s's key can add backups but not erase them: what it deletes is only hidden.", capital(name))
	switch days := m.keepsHidden(ctx, s3, folder); {
	case days < 0:
		act.Printf("Roostack couldn't read how long this bucket keeps hidden files.")
	case days == 0:
		act.Printf("This bucket keeps hidden files for good. Old backups the controller removes still take space until a lifecycle rule erases them.")
	case days < MinKeepHidden:
		after := fmt.Sprintf("after %d days", days)
		if days == 1 {
			after = "after a day"
		}
		act.Printf("This bucket erases hidden files %s, so a server that's taken over could still destroy its backups. "+
			"In Backblaze's settings, keep old versions for at least %d days.", after, MinKeepHidden)
	default:
		act.Printf("This bucket keeps hidden files for %d days.", days)
	}
}

// b2 is a Backblaze client for an account.
func (m *Manager) b2(s3 S3Provider) *b2.Client {
	if m.NewB2 != nil {
		return m.NewB2(s3.AccessKey, s3.SecretKey)
	}
	return &b2.Client{KeyID: s3.AccessKey, Key: s3.SecretKey}
}

// forgetServerBackups deletes provider keys made for a server that left
// the lab, and says in its backup log which shared credentials it still
// holds (M22 of the 2026-10-16 review). Its own backups stay where the
// controller can still reach them (the recovery key still reads them, and
// a change of recovery key reaches them). Backups kept on its disk are
// forgotten, its own and other servers': nothing can reach them now, and
// a change of recovery key would wait for them for ever.
func (m *Manager) forgetServerBackups(ctx context.Context, serverID string) {
	if m.BackupStore == nil {
		return
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return
	}
	copies, _ := m.BackupStore.BackupCopies(ctx)
	m.BackupStore.RemoveLostBackup(ctx, serverID)
	name := m.nameOf(serverID)
	act := m.Activity.Start(activity.BackupSetupFor(serverID), "Taking "+name+"'s backup access away")
	defer act.Finish(nil)
	for _, c := range m.SharedCredentials(ctx, serverID) {
		if c.Anywhere {
			act.Printf("%s still holds %s, which reaches every server's backups there and can delete them. Change it.", name, c.What)
		} else {
			act.Printf("%s still knows %s, but can't reach it without this lab's network.", name, c.What)
		}
	}
	m.removeRemoteUsers(ctx, act, serverID)
	for _, t := range targets {
		if t.StoreHost != serverID {
			continue
		}
		if t.ServerID != serverID {
			// Its plan and "needs attention" say so until it has a new
			// one, rather than its nightly backups failing unexplained.
			act.Printf("%s's main backup was on %s's disk: choose somewhere else on its Backups page.", m.nameOf(t.ServerID), name)
			lost := store.LostBackup{ServerID: t.ServerID, HostName: name, Lost: time.Now()}
			if err := m.BackupStore.SetLostBackup(ctx, lost); err != nil {
				m.Log.Warn("recording a lost main backup", "server", t.ServerID, "err", err)
			}
		}
		if err := m.BackupStore.RemoveBackupTarget(ctx, t.ServerID); err != nil {
			m.Log.Warn("forgetting backups on a removed server", "server", name, "err", err)
		}
	}
	for _, t := range copies {
		if t.ServerID != serverID && t.StoreHost != serverID {
			continue
		}
		if t.ServerID != serverID {
			// The other server stops copying there (it restarts Roostack).
			act.Printf("%s's backups were also copied to %s's disk: that copy stops.", m.nameOf(t.ServerID), name)
			err := m.RemoveBackupCopy(ctx, t.ServerID, t.Provider)
			if err == nil {
				continue
			}
			act.Printf("%s couldn't be told (%v), so its nightly copy there fails until its backups change on its Backups page.",
				m.nameOf(t.ServerID), firstLine(err.Error()))
		}
		m.BackupStore.RemoveBackupCopy(ctx, t.ServerID, t.Provider)
	}
	for _, t := range append(targets, copies...) {
		if t.ServerID != serverID || t.ScopedKey == "" {
			continue
		}
		d, err := m.destinationOf(ctx, t.Provider)
		if err != nil || !d.isB2() {
			continue
		}
		act.Step("Deleting %s's Backblaze key", name)
		if err := m.b2(d.s3).DeleteKey(ctx, t.ScopedKey); err != nil {
			act.Printf("%v", err)
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
	pw, hash, err := labnet.NewPassword()
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

// alone says whether the lab has one server. Its pages and logs don't name
// it: the name is the host's, which the Owner may never have chosen
// (found 2026-10-18).
func (m *Manager) alone() bool { return len(m.others()) == 0 }

// serverName is a server's name in a sentence, or "this server" in a lab
// of one.
func (m *Manager) serverName(id string) string {
	if id == m.Lab.NodeID && m.alone() {
		return "this server"
	}
	return m.nameOf(id)
}

// onServer is " on <server>" after a step that happens on one, or nothing
// in a lab of one.
func (m *Manager) onServer(id string) string {
	if id == m.Lab.NodeID && m.alone() {
		return ""
	}
	return " on " + m.nameOf(id)
}

// aboutServer starts a line about one server with its name ("nas:
// keeping …"), or, in a lab of one, is the line alone ("Keeping …").
func (m *Manager) aboutServer(id, line string) string {
	if id == m.Lab.NodeID && m.alone() {
		if line == "" {
			return line
		}
		return strings.ToUpper(line[:1]) + line[1:]
	}
	return m.nameOf(id) + ": " + line
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
		title = m.destinationTitle(ctx, serverID, x.Provider, x.Repository, choices)
		return x, title, true
	}
	return t, "", false
}

// destinationTitle names where a server's backups go, for people.
func (m *Manager) destinationTitle(ctx context.Context, serverID, provider, repository string, choices []BackupChoice) string {
	title := repository
	switch {
	case destination.Parse(provider).Kind == destination.Local, provider == destination.Setup && strings.HasPrefix(repository, "/"):
		title = "A folder on " + m.serverName(serverID)
	case provider == destination.Setup:
		title = "Chosen during setup"
	}
	for _, c := range choices {
		if c.Ref == provider {
			title = c.Title
		}
	}
	if t := m.driveTitle(ctx, serverID, provider); t != "" {
		title = t
	}
	return title
}

// PruneJob removes old backups from every repository the controller made
// (SPEC.md §10.10): servers may only add backups, so the controller does
// this with the recovery key, weekly. A repository that's locked (its
// server still backing up, e.g. a big first backup) is tried again an
// hour later, up to pruneTries times in all.
func (m *Manager) PruneJob() maintenance.Job { return m.pruneJob(nil, 1) }

// pruneTries is how often a locked repository is tried in a week, and
// pruneRetry how long apart.
var (
	pruneTries = 3
	pruneRetry = time.Hour
)

// pruneKey names a repository in a prune (a server's backup or copy).
func pruneKey(t store.BackupTarget) string { return t.ServerID + " " + t.Provider }

// pruneJob prunes the repositories in only (all if nil); try counts.
func (m *Manager) pruneJob(only map[string]bool, try int) maintenance.Job {
	title := "Removing old backups of every server"
	if try > 1 {
		title = "Removing old backups that were busy last time"
	}
	return maintenance.Job{
		Name: "removing old backups of every server",
		Run: func(ctx context.Context, h *maintenance.Handle) (err error) {
			act := m.Activity.Start(activity.Backup, title)
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
			locked := map[string]bool{}
			m.recheckS3(ctx, act, append(targets, copies...))
			for _, t := range append(targets, copies...) {
				if destination.Parse(t.Provider).Kind == destination.Setup || (only != nil && !only[pruneKey(t)]) {
					continue // set up before this, that server prunes it itself; or not a retry's
				}
				repo, err := m.adminRepo(ctx, t)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				act.Step("%s", m.aboutServer(t.ServerID, "keeping 7 daily, 4 weekly and 6 monthly"))
				// Stale locks first, or one crashed backup blocks every
				// prune there (docs/ASSUMPTIONS.md N1); a running
				// backup's lock stays.
				if n, err := repo.Unlock(ctx); err != nil {
					act.Printf("Couldn't clear old locks: %s", firstLine(err.Error()))
				} else if n == 1 {
					act.Printf("Cleared an old lock left by a backup that didn't finish")
				} else if n > 1 {
					act.Printf("Cleared %d old locks left by backups that didn't finish", n)
				}
				err = repo.Forget(ctx, backup.DefaultRetention)
				switch {
				case errors.Is(err, backup.ErrLocked) && try < pruneTries && m.Submit != nil:
					act.Printf("Busy (a backup is running there): tried again in %s", pruneRetry)
					locked[pruneKey(t)] = true
				case err != nil:
					act.Printf("%v", err)
					errs = append(errs, errors.New(m.aboutServer(t.ServerID, err.Error())))
				}
			}
			if len(locked) > 0 {
				time.AfterFunc(pruneRetry, func() { m.Submit(m.pruneJob(locked, try+1)) })
			}
			return errors.Join(errs...)
		},
	}
}

// adminRepo opens a server's repository as the controller: through the
// store's admin access for a server's disk, or the provider's
// credentials.
func (m *Manager) adminRepo(ctx context.Context, t store.BackupTarget) (*backup.Repo, error) {
	d, err := m.destinationOf(ctx, t.Provider)
	if err != nil {
		return nil, err
	}
	switch d.kind {
	case destFolder, destDrive: // a drive or folder on the controller
		return m.OpenRepo(t.Repository, nil), nil
	case destCloud:
		r := m.OpenRepo(t.Repository, nil)
		r.Credentials = m.cloudPruneKeys().Env
		return r, nil
	case destServer:
		pw, _, ok, err := m.BackupStore.StoreAdmin(ctx, t.StoreHost)
		if err != nil || !ok {
			return nil, fmt.Errorf("no admin access to %s's backup store", m.nameOf(t.StoreHost))
		}
		url := strings.Replace(t.Repository, fmt.Sprintf(":%d/", generate.StoragePort), fmt.Sprintf(":%d/", generate.StorageAdminPort), 1)
		return m.OpenRepo(url, []string{"RESTIC_REST_USERNAME=controller", "RESTIC_REST_PASSWORD=" + pw}), nil
	case destRemote:
		url, env := remoteAdmin(d.remote, t.Repository)
		return m.OpenRepo(url, env), nil
	case destS3:
		// The account's key: it can erase what servers hide.
		return m.OpenRepo(t.Repository, d.s3.Env()), nil
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
	return usererr.New("Set up this server's backups first: that creates the recovery key every backup can be read with.")
}

// choiceFor finds a place a server can back up: one of BackupChoices, or a
// drive ("drive:<id>") or folder ("folder:<path>") on this server.
func (m *Manager) choiceFor(ctx context.Context, serverID, ref string) (*BackupChoice, error) {
	r := destination.Parse(ref)
	switch r.Kind {
	case destination.Drive:
		if serverID != m.Lab.NodeID || m.Drives == nil {
			return nil, usererr.New("A drive can only hold backups of the server it's in.")
		}
		drives, err := m.Drives.Drives(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range drives {
			if d.ID == r.ID && d.Usable {
				return &BackupChoice{Ref: ref, Title: DriveTitle(d)}, nil
			}
		}
		return nil, usererr.New("That drive isn't connected any more.")
	case destination.Folder:
		if serverID != m.Lab.NodeID || m.Drives == nil || !filepath.IsAbs(r.ID) {
			return nil, usererr.New("Enter a full folder path, starting with /.")
		}
		return &BackupChoice{Ref: destination.New(destination.Folder, filepath.Clean(r.ID)), Title: "The folder " + filepath.Clean(r.ID)}, nil
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
	switch r := destination.Parse(ref); r.Kind {
	case destination.Drive:
		if c, err := m.choiceFor(ctx, serverID, ref); err == nil && c != nil {
			return c.Title
		}
		return "A drive that isn't connected now"
	case destination.Folder:
		return "The folder " + r.ID
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

// recheckS3 checks again, weekly, whether each S3 bucket (not Backblaze)
// the lab backs up to keeps what's deleted (S3Check): its settings or
// the key's permissions may have changed.
func (m *Manager) recheckS3(ctx context.Context, act *activity.Log, targets []store.BackupTarget) {
	seen := map[string]bool{}
	for _, t := range targets {
		d, err := m.destinationOf(ctx, t.Provider)
		if err != nil || d.kind != destS3 || d.isB2() || seen[t.Provider] {
			continue
		}
		seen[t.Provider] = true
		act.Step("Checking whether %s keeps what's deleted", d.provider.Name)
		sayS3Check(act, m.checkS3(ctx, d.s3))
	}
}
