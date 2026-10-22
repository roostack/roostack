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
	"sync"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
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
// is a request over Roostack's network (SPEC.md §10.3). The operations
// are in the wire files (labwire.go, backupwire.go...), each beside the
// Server routes that answer them.
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
		err := r.call(context.Background(), http.MethodPut, assignmentPath, w, nil)
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
	err := r.call(ctx, http.MethodGet, statusPath, nil, &st)
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

// call makes a lab API request to the server.
func (r *Remote) call(ctx context.Context, method, path string, in, out any) error {
	r.mu.Lock()
	down := r.down() && path != statusPath
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
