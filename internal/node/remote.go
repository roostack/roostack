package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/osupdate"
)

// Timeouts for calls to another server.
const (
	statusTimeout = 4 * time.Second
	statusCache   = 5 * time.Second
	// downFor: once a server stops answering, calls to it fail at once for
	// this long, while a background check sees if it's back. Otherwise every
	// page would wait seconds for each call to a server that's off.
	downFor   = 30 * time.Second
	pushRetry = 30 * time.Second
)

// Remote is another server, as the controller sees it: every operation
// is a request over Roostack's network (SPEC.md §10.3).
type Remote struct {
	ID   nodeid.Identity
	Addr netip.Addr
	// Lab is the controller's lab config (to read package files).
	Lab    config.Lab
	Client *http.Client
	Log    *slog.Logger
	// URL is where the server's lab API is; set by NewRemote.
	URL string

	mu sync.Mutex
	// latest is the newest assignment, kept until the node has it; its
	// secrets are dropped once delivered.
	latest    *WireAssignment
	delivered int64
	pushing   bool
	status    WireStatus
	statusErr error
	statusAt  time.Time
	// downAt is when a call last failed to get through (zero while it
	// answers); probing: a background check is asking it.
	downAt  time.Time
	probing bool
	// closed: it left the lab; nothing more is sent to it.
	closed bool
	// reads caches answers to cachedReads (readCache).
	reads map[string]cachedRead
}

// Reads the pages refresh every few seconds (home, Services, Settings)
// are shared for readCache: ten open tabs cost one call, not ten. Any
// change (a call that isn't a GET) forgets them, so a page shows what it
// just did. Live logs, install progress, restores and data aren't cached.
const readCache = 10 * time.Second

var cachedReads = map[string]bool{
	"/v1/services": true, "/v1/services/details": true, "/v1/builtins": true, "/v1/backups": true,
	"/v1/disks": true, "/v1/settings": true, "/v1/updates": true, "/v1/host/changes": true, "/v1/kept": true,
}

type cachedRead struct {
	at     time.Time
	status int
	header http.Header
	body   []byte
}

// NewRemote returns a server reached with this server's certificate.
func NewRemote(self labnet.Identity, id nodeid.Identity, addr netip.Addr, lab config.Lab, log *slog.Logger) (*Remote, error) {
	tlsConf, err := self.ClientTLS(id.ID)
	if err != nil {
		return nil, err
	}
	return &Remote{ID: id, Addr: addr, Lab: lab, Log: log, URL: "https://" + netip.AddrPortFrom(addr, labnet.Port).String(),
		Client: &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{TLSClientConfig: tlsConf}}}, nil
}

func (r *Remote) Identity() nodeid.Identity { return r.ID }

// Leave asks the server to leave the lab: it takes down its WireGuard
// interface and becomes a lab of its own.
func (r *Remote) Leave(ctx context.Context) error {
	return r.call(ctx, http.MethodPost, "/v1/leave", struct{}{}, nil)
}

// Rename tells the server its new name.
func (r *Remote) Rename(ctx context.Context, name string) error {
	return r.call(ctx, http.MethodPut, "/v1/name", map[string]string{"Name": name}, nil)
}

// Address is the server's address on Roostack's network.
func (r *Remote) Address() string { return r.Addr.String() }

// Apply sends the server its assignment in the background, retrying
// until it has it. A server also fetches it when it starts (Pending).
func (r *Remote) Apply(a Assignment) {
	w, err := Encode(r.Lab, a, time.Now().UnixNano())
	if err != nil {
		r.Log.Error("encoding assignment", "server", r.ID.Name, "err", err)
		return
	}
	// The controller hands every server its assignment on every change;
	// one that already has this one, and applied it fine, has nothing to
	// do (it would only look busy). One whose last apply failed gets it
	// again: that's its retry.
	if r.unchanged(w) {
		if st, err := r.wireStatus(); err == nil && st.apply().LastErr == nil {
			return
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	// Typed-in secrets not yet delivered are still needed.
	if r.latest != nil && len(r.latest.Secrets) > 0 {
		merged := map[string][]byte{}
		for k, v := range r.latest.Secrets {
			merged[k] = v
		}
		for k, v := range w.Secrets {
			merged[k] = v
		}
		w.Secrets = merged
		w.SetUp = append(append([]string(nil), r.latest.SetUp...), w.SetUp...)
	}
	r.latest = &w
	if !r.pushing {
		r.pushing = true
		go r.push()
	}
}

// Close stops sending it assignments: it left the lab (a retry still
// waiting gives up).
func (r *Remote) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

// unchanged reports whether w is what the server already has: the
// latest assignment, delivered, with nothing new to hand over (secrets,
// set-ups).
func (r *Remote) unchanged(w WireAssignment) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest == nil || r.latest.Version != r.delivered || len(w.Secrets) > 0 || len(w.SetUp) > 0 {
		return false
	}
	old, next := *r.latest, w
	old.Version, next.Version = 0, 0
	old.Reason, next.Reason = "", "" // why doesn't change what
	a, err1 := json.Marshal(old)
	b, err2 := json.Marshal(next)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}

func (r *Remote) push() {
	for {
		r.mu.Lock()
		w := *r.latest
		r.mu.Unlock()
		err := r.call(context.Background(), http.MethodPut, "/v1/assignment", w, nil)
		r.mu.Lock()
		if err == nil {
			r.deliveredLocked(w.Version)
		}
		if r.latest.Version == r.delivered || r.closed {
			r.pushing = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if err != nil {
			r.Log.Warn("server didn't take its assignment; retrying", "server", r.ID.Name, "err", err)
			time.Sleep(pushRetry)
		}
	}
}

// deliveredLocked records that the server has version v: its secrets
// and set-ups needn't be sent again.
func (r *Remote) deliveredLocked(v int64) {
	r.delivered = max(r.delivered, v)
	if r.latest != nil && r.latest.Version == v {
		r.latest.Secrets, r.latest.SetUp = nil, nil
	}
	r.statusAt = time.Time{} // refresh
}

// Pending returns the latest assignment for the server to fetch, and
// counts it as delivered.
func (r *Remote) Pending() (WireAssignment, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest == nil {
		return WireAssignment{}, false
	}
	w := *r.latest
	r.deliveredLocked(w.Version)
	return w, true
}

// Status is the server's apply status; an unreachable server reports
// why as its last error.
func (r *Remote) Status() ApplyStatus {
	st, err := r.wireStatus()
	if err != nil {
		// ErrUnreachable whatever the cause, so it's told apart from a change
		// that failed (the cause is in the log).
		return ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach %s: %w", r.ID.Name, ErrUnreachable)}
	}
	return st.apply()
}

// Reachable reports whether the server answered recently.
func (r *Remote) Reachable() bool {
	_, err := r.wireStatus()
	return err == nil
}

func (r *Remote) wireStatus() (WireStatus, error) {
	r.mu.Lock()
	if time.Since(r.statusAt) < statusCache {
		st, err := r.status, r.statusErr
		r.mu.Unlock()
		return st, err
	}
	if r.down() {
		// Don't wait for it: check in the background, answer at once.
		if !r.probing {
			r.probing = true
			go func() {
				r.fetchStatus()
				r.mu.Lock()
				r.probing = false
				r.mu.Unlock()
			}()
		}
		err := r.unreachable()
		r.mu.Unlock()
		return WireStatus{}, err
	}
	r.mu.Unlock()
	return r.fetchStatus()
}

// fetchStatus asks the server for its status, and caches the answer.
func (r *Remote) fetchStatus() (WireStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	var st WireStatus
	err := r.call(ctx, http.MethodGet, "/v1/status", nil, &st)
	r.mu.Lock()
	r.status, r.statusErr, r.statusAt = st, err, time.Now()
	r.mu.Unlock()
	return st, err
}

// ErrUnreachable means another server doesn't answer.
var ErrUnreachable = errors.New("Roostack can't reach it")

// down reports whether calls to the server stopped getting through lately
// (r.mu held).
func (r *Remote) down() bool { return !r.downAt.IsZero() && time.Since(r.downAt) < downFor }

// unreachable is the error for a call while the server is down.
func (r *Remote) unreachable() error {
	return fmt.Errorf("Roostack can't reach %s right now: %w", r.ID.Name, ErrUnreachable)
}

func (r *Remote) ServiceStates(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out map[string]string
	err := r.call(ctx, http.MethodGet, "/v1/services", nil, &out)
	return out, err
}

func (r *Remote) LatestActivity(kind string) (activity.Snapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	var snap activity.Snapshot
	err := r.call(ctx, http.MethodGet, "/v1/activity/"+url.PathEscape(kind), nil, &snap)
	return snap, err == nil
}

func (r *Remote) InstallStatus(ctx context.Context, service string) HookStatus {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var st HookStatus
	if err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/setup", nil, &st); err != nil {
		return HookStatus{Failed: err.Error()}
	}
	return st
}

func (r *Remote) RetryInstall(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/setup/retry", struct{}{}, nil)
}

func (r *Remote) RunAction(ctx context.Context, service, action string, inputs map[string]string) (string, error) {
	var out struct{ Output string }
	err := r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/actions/"+url.PathEscape(action), inputs, &out)
	return out.Output, err
}

func (r *Remote) RevealSecret(ctx context.Context, service, key string) (string, error) {
	var out struct{ Value string }
	err := r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/secrets/"+url.PathEscape(key), struct{}{}, &out)
	return out.Value, err
}

// Maintenance and settings are offered if the server has them (as of its
// last status).
func (r *Remote) Backups() Backups {
	if st, err := r.wireStatus(); err != nil || !st.Backups {
		return nil
	}
	return remoteBackups{r}
}

func (r *Remote) Restores() Restores {
	if st, err := r.wireStatus(); err != nil || !st.Restores {
		return nil
	}
	return remoteRestores{r}
}

func (r *Remote) Updates() Updates {
	if st, err := r.wireStatus(); err != nil || !st.Updates {
		return nil
	}
	return remoteUpdates{r}
}

func (r *Remote) Settings() Settings {
	if st, err := r.wireStatus(); err != nil || !st.Settings {
		return nil
	}
	return remoteSettings{r}
}

type remoteBackups struct{ r *Remote }

func (b remoteBackups) Status(ctx context.Context) (backup.Status, error) {
	var st backup.Status
	err := b.r.call(ctx, http.MethodGet, "/v1/backups", nil, &st)
	return st, err
}

func (b remoteBackups) RunNow(ctx context.Context) error {
	return b.r.call(ctx, http.MethodPost, "/v1/backups/run", struct{}{}, nil)
}

type remoteRestores struct{ r *Remote }

func (x remoteRestores) Snapshots(ctx context.Context, service string) ([]backup.Snapshot, error) {
	var out []backup.Snapshot
	err := x.r.call(ctx, http.MethodGet, "/v1/restores/"+url.PathEscape(service)+"/snapshots", nil, &out)
	return out, err
}

func (x remoteRestores) Restore(ctx context.Context, service, snapshotID string) error {
	return x.r.call(ctx, http.MethodPost, "/v1/restores/"+url.PathEscape(service),
		map[string]string{"Snapshot": snapshotID}, nil)
}

func (x remoteRestores) Pending(ctx context.Context) (bool, error) {
	var out bool
	err := x.r.call(ctx, http.MethodGet, "/v1/restores/pending", nil, &out)
	return out, err
}

func (x remoteRestores) Kept(ctx context.Context, service string) ([]backup.KeptCopy, error) {
	var out []backup.KeptCopy
	err := x.r.call(ctx, http.MethodGet, "/v1/restores/"+url.PathEscape(service)+"/kept", nil, &out)
	return out, err
}

func (x remoteRestores) PutBack(ctx context.Context, service, path string) error {
	return x.r.call(ctx, http.MethodPost, "/v1/restores/"+url.PathEscape(service)+"/kept/put-back",
		map[string]string{"Path": path}, nil)
}

func (x remoteRestores) RemoveKept(ctx context.Context, service, path string) error {
	return x.r.call(ctx, http.MethodPost, "/v1/restores/"+url.PathEscape(service)+"/kept/remove",
		map[string]string{"Path": path}, nil)
}

type remoteUpdates struct{ r *Remote }

func (u remoteUpdates) get(ctx context.Context) (wireUpdates, error) {
	var w wireUpdates
	err := u.r.call(ctx, http.MethodGet, "/v1/updates", nil, &w)
	return w, err
}

func (u remoteUpdates) Last(ctx context.Context) (*osupdate.Result, error) {
	w, err := u.get(ctx)
	return w.Last, err
}

func (u remoteUpdates) Pending(ctx context.Context) (bool, error) {
	w, err := u.get(ctx)
	return w.Pending, err
}

func (u remoteUpdates) Submit(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/updates/run", struct{}{}, nil)
}

func (u remoteUpdates) RestartStatus(ctx context.Context) (osupdate.RestartStatus, error) {
	w, err := u.get(ctx)
	return w.Restart, err
}

func (u remoteUpdates) HostChanges(ctx context.Context) (osupdate.HostCheck, error) {
	var c osupdate.HostCheck
	err := u.r.call(ctx, http.MethodGet, "/v1/host/changes", nil, &c)
	return c, err
}

func (u remoteUpdates) AcknowledgeHostChanges(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/host/changes/known", struct{}{}, nil)
}

func (u remoteUpdates) Series(ctx context.Context) (osupdate.Series, error) {
	var s osupdate.Series
	err := u.r.call(ctx, http.MethodGet, "/v1/updates/series", nil, &s)
	return s, err
}

func (u remoteUpdates) AcceptSeries(ctx context.Context, series string) error {
	return u.r.call(ctx, http.MethodPost, "/v1/updates/series", map[string]string{"Series": series}, nil)
}

func (u remoteUpdates) RestartNow(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/restart", struct{}{}, nil)
}

type remoteSettings struct{ r *Remote }

func (x remoteSettings) get(ctx context.Context) (wireSettings, error) {
	var w wireSettings
	err := x.r.call(ctx, http.MethodGet, "/v1/settings", nil, &w)
	return w, err
}

func (x remoteSettings) Timezones(ctx context.Context) (string, []string, error) {
	w, err := x.get(ctx)
	return w.Timezone, w.Zones, err
}

func (x remoteSettings) SetTimezone(ctx context.Context, name string) (bool, error) {
	var out struct{ Changed bool }
	err := x.r.call(ctx, http.MethodPut, "/v1/settings/timezone", map[string]string{"Timezone": name}, &out)
	return out.Changed, err
}

func (x remoteSettings) SSHAccess(ctx context.Context, fresh bool) (SSHAccess, error) {
	if fresh {
		var a SSHAccess
		err := x.r.call(ctx, http.MethodGet, "/v1/settings/ssh", nil, &a)
		return a, err
	}
	w, err := x.get(ctx)
	return w.SSH, err
}

func (x remoteSettings) SetSSH(ctx context.Context, c SSHChange) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/ssh", c, nil)
}

func (x remoteSettings) PromoteBackupCopy(ctx context.Context, name string, old BackupCopy) error {
	return x.r.call(ctx, http.MethodPost, "/v1/settings/backup-copies/promote",
		map[string]any{"name": name, "old": old}, nil)
}

func (x remoteSettings) SetBackupCopies(ctx context.Context, copies []BackupCopy) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/backup-copies", copies, nil)
}

func (x remoteSettings) SetBackupDestination(ctx context.Context, d BackupDestination) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/backup-destination", d, nil)
}

func (x remoteSettings) RestartPolicy(ctx context.Context) (string, int, error) {
	w, err := x.get(ctx)
	return w.RestartMode, w.RestartHour, err
}

func (x remoteSettings) SetRestartPolicy(ctx context.Context, mode string, hour int) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/restart-policy", map[string]any{"Mode": mode, "Hour": hour}, nil)
}

// call makes a lab API request to the server.
func (r *Remote) call(ctx context.Context, method, path string, in, out any) error {
	r.mu.Lock()
	down := r.down() && path != "/v1/status"
	r.mu.Unlock()
	if down {
		return r.unreachable()
	}
	cacheable := method == http.MethodGet && cachedReads[path]
	if cacheable {
		r.mu.Lock()
		c, ok := r.reads[path]
		r.mu.Unlock()
		if ok && time.Since(c.at) < readCache {
			return wireError(labnet.DecodeResponse(c.response(), out))
		}
	} else if method != http.MethodGet {
		r.mu.Lock()
		r.reads = nil
		r.mu.Unlock()
	}
	var body *bytes.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.Client.Do(req)
	r.mu.Lock()
	switch {
	case err == nil:
		r.downAt = time.Time{}
	case ctx.Err() != context.Canceled:
		// It didn't answer (not the caller giving up).
		r.downAt = time.Now()
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if cacheable {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return err
		}
		c := cachedRead{at: time.Now(), status: resp.StatusCode, header: resp.Header, body: data}
		if resp.StatusCode < 500 {
			r.mu.Lock()
			if r.reads == nil {
				r.reads = map[string]cachedRead{}
			}
			r.reads[path] = c
			r.mu.Unlock()
		}
		return wireError(labnet.DecodeResponse(c.response(), out))
	}
	return wireError(labnet.DecodeResponse(resp, out))
}

// response replays a cached answer.
func (c cachedRead) response() *http.Response {
	return &http.Response{StatusCode: c.status, Header: c.header, Body: io.NopCloser(bytes.NewReader(c.body))}
}

// Moving a service's data (Mover, over the lab API).

// streamClient is for data streams, which can take hours: no overall
// timeout (the caller's context stops them).
func (r *Remote) streamClient() *http.Client {
	return &http.Client{Transport: r.Client.Transport}
}

func (r *Remote) DataDirs(string) []string { return nil } // only the server itself knows

func (r *Remote) ExportData(ctx context.Context, service string, dir int, since time.Time, w io.Writer) error {
	q := url.Values{"dir": {strconv.Itoa(dir)}}
	if !since.IsZero() {
		q.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL+"/v1/services/"+url.PathEscape(service)+"/data?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := r.streamClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return wireError(labnet.DecodeResponse(resp, nil))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (r *Remote) ImportData(ctx context.Context, service string, dir int, body io.Reader, strip int) error {
	u := r.URL + "/v1/services/" + url.PathEscape(service) + "/data?dir=" + strconv.Itoa(dir) + "&strip=" + strconv.Itoa(strip)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := r.streamClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return wireError(labnet.DecodeResponse(resp, nil))
}

func (r *Remote) ListData(ctx context.Context, service string, dir int) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/data/list?dir="+strconv.Itoa(dir), nil, &out)
	return out, err
}

func (r *Remote) PruneData(ctx context.Context, service string, dir int, keep []string) error {
	return r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/data/prune?dir="+strconv.Itoa(dir), keep, nil)
}

func (r *Remote) CommitData(ctx context.Context, service string) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/data/commit", struct{}{}, &out)
	return out, err
}

func (r *Remote) SetAsideData(ctx context.Context, service, note string) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/data/set-aside", map[string]string{"Note": note}, &out)
	return out, err
}

func (r *Remote) DataSize(ctx context.Context, service string) (int64, error) {
	var out int64
	err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/data/size", nil, &out)
	return out, err
}

func (r *Remote) StopService(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/stop", struct{}{}, nil)
}

func (r *Remote) StartService(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/start", struct{}{}, nil)
}

func (r *Remote) ExportSecrets(ctx context.Context, service string) (map[string][]byte, error) {
	var out map[string][]byte
	err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/secrets", nil, &out)
	return out, err
}

func (r *Remote) ImportSecrets(ctx context.Context, secrets map[string][]byte) error {
	return r.call(ctx, http.MethodPut, "/v1/secrets", secrets, nil)
}

func (r *Remote) LoadDumps(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, "/v1/services/"+url.PathEscape(service)+"/data/load-dumps", struct{}{}, nil)
}

func (r *Remote) KeptData(ctx context.Context) ([]KeptFolder, error) {
	var out []KeptFolder
	err := r.call(ctx, http.MethodGet, "/v1/kept", nil, &out)
	return out, err
}

func (r *Remote) PutBackKeptData(ctx context.Context, path string) error {
	return r.call(ctx, http.MethodPost, "/v1/kept/put-back", map[string]string{"Path": path}, nil)
}

func (r *Remote) RemoveKeptData(ctx context.Context, path string) error {
	return r.call(ctx, http.MethodPost, "/v1/kept/remove", map[string]string{"Path": path}, nil)
}

func (r *Remote) ServiceDetails(ctx context.Context) (map[string]ServiceDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out map[string]ServiceDetail
	err := r.call(ctx, http.MethodGet, "/v1/services/details", nil, &out)
	return out, err
}

func (r *Remote) BuiltIns(ctx context.Context) ([]BuiltIn, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out []BuiltIn
	err := r.call(ctx, http.MethodGet, "/v1/builtins", nil, &out)
	return out, err
}

// Updating a service (Updater, over the lab API).

func (r *Remote) StartUpdate(ctx context.Context, req UpdateRequest) (string, error) {
	var id string
	err := r.call(ctx, http.MethodPost, "/v1/updates", req, &id)
	return id, err
}

func (r *Remote) UpdateStatus(ctx context.Context, id string) (UpdateStatus, error) {
	var out UpdateStatus
	err := r.call(ctx, http.MethodGet, "/v1/updates/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (r *Remote) FinishUpdate(ctx context.Context, id string) error {
	return r.call(ctx, http.MethodPost, "/v1/updates/"+url.PathEscape(id)+"/finish", struct{}{}, nil)
}

func (r *Remote) CheckHealth(ctx context.Context, service string) (Health, error) {
	var out Health
	err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/health", nil, &out)
	return out, err
}
