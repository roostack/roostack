package node

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// What crosses Roostack's network between the controller and a node (SPEC.md
// §10.3): plain data, the same operations the in-process Local offers.

// WireAssignment is an Assignment on the wire. Services travel as their
// instance and package files, so the node needs no catalog of its own.
type WireAssignment struct {
	// Version increases with every assignment; a node ignores older ones.
	Version  int64             `json:"version"`
	Lab      LabContext        `json:"lab"`
	Services []WireService     `json:"services"`
	Secrets  map[string][]byte `json:"secrets,omitempty"`
	SetUp    []string          `json:"set_up,omitempty"`
	Proxied  []WireProxied     `json:"proxied,omitempty"`
	Storage  *Storage          `json:"storage,omitempty"`
	// Guests are other labs' stores, when this server offers storage for
	// them, on its address GuestAddr on the guests' link.
	Guests    []generate.GuestStorage `json:"guests,omitempty"`
	GuestAddr string                  `json:"guest_addr,omitempty"`
	// Links are its direct links to other servers (nil: unchanged).
	Links *agent.Links `json:"links,omitempty"`
	// Edge makes it the lab's edge (SPEC.md §10.5).
	Edge *caddy.EdgeConfig `json:"edge,omitempty"`
	// Libraries are its media libraries (SPEC.md §8.1).
	Libraries []agent.Library `json:"libraries,omitempty"`
	// Reason says why it was sent, for the apply's log.
	Reason string `json:"reason,omitempty"`
}

// LabContext is the lab-wide settings a node's services need.
type LabContext struct {
	Domain string `json:"domain"`
	Email  string `json:"email,omitempty"`
	// NoSharing is the Owner's CrowdSec opt-out (config.Public), for the
	// server running Caddy.
	NoSharing bool `json:"no_sharing,omitempty"`
	// EdgeAddrs are the lab's edges' WireGuard addresses (config.Lab), for
	// the server running Caddy.
	EdgeAddrs []string `json:"edge_addrs,omitempty"`
}

// WireService is a service's instance and package files.
type WireService struct {
	Instance []byte `json:"instance"`
	Package  []byte `json:"package"`
	// Stop: the controller has stopped it (state.Service.Stop).
	Stop *state.Stop `json:"stop,omitempty"`
}

// WireProxied is a service whose routes the receiving server serves for
// another server.
type WireProxied struct {
	WireService
	Addr string `json:"addr"`
}

// Encode turns an assignment into wire form, reading each package file
// from the lab's own copy (SPEC.md §8.3.1).
func Encode(lab config.Lab, a Assignment, version int64) (WireAssignment, error) {
	w := WireAssignment{Version: version, Lab: a.Lab,
		Secrets: a.Secrets, SetUp: a.SetUp, Storage: a.Storage, Guests: a.Guests, GuestAddr: a.GuestAddr, Links: a.Links, Edge: a.Edge,
		Libraries: a.Libraries, Reason: a.Reason}
	enc := func(s state.Service) (WireService, error) {
		in, err := s.Instance.Marshal()
		if err != nil {
			return WireService{}, err
		}
		pkg, err := os.ReadFile(lab.Paths.LabPackageFile(s.Instance.Package, s.Instance.Version))
		if err != nil {
			return WireService{}, err
		}
		return WireService{Instance: in, Package: pkg, Stop: s.Stop}, nil
	}
	for _, s := range a.Services {
		ws, err := enc(s)
		if err != nil {
			return w, err
		}
		w.Services = append(w.Services, ws)
	}
	for _, p := range a.Proxied {
		ws, err := enc(p.Service)
		if err != nil {
			return w, err
		}
		w.Proxied = append(w.Proxied, WireProxied{WireService: ws, Addr: p.Addr})
	}
	return w, nil
}

// Decode turns a wire assignment back into one, checking every file.
func Decode(w WireAssignment) (Assignment, error) {
	a := Assignment{Lab: w.Lab, Secrets: w.Secrets, SetUp: w.SetUp, Storage: w.Storage, Guests: w.Guests, GuestAddr: w.GuestAddr,
		Links: w.Links, Edge: w.Edge, Libraries: w.Libraries, Reason: w.Reason}
	dec := func(ws WireService) (state.Service, error) {
		p, err := pkgspec.Parse(ws.Package)
		if err != nil {
			return state.Service{}, err
		}
		in, err := instance.Parse(ws.Instance)
		if err != nil {
			return state.Service{}, err
		}
		if in.Package != p.Name {
			return state.Service{}, fmt.Errorf("instance of %q with package %q", in.Package, p.Name)
		}
		if err := in.Validate(p); err != nil {
			return state.Service{}, err
		}
		// The controller checked the package policy before installing it;
		// a node checks again rather than trust that (SPEC.md §8.3.4).
		// Only a package the Owner added themselves may break it, once
		// they've accepted what it asks for.
		if risks := p.Risks(); len(risks) > 0 && !in.Local {
			return state.Service{}, fmt.Errorf("%s: package %s %s breaks the package policy, so this server won't run it: %s",
				in.ID(), p.Name, p.Version, strings.Join(risks, " "))
		}
		return state.Service{Instance: in, Package: p, Stop: ws.Stop}, nil
	}
	var errs []error
	for _, ws := range w.Services {
		s, err := dec(ws)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a.Services = append(a.Services, s)
	}
	for _, wp := range w.Proxied {
		s, err := dec(wp.WireService)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a.Proxied = append(a.Proxied, state.Proxied{Service: s, Addr: wp.Addr})
	}
	return a, errors.Join(errs...)
}

// WireStatus is a node's apply status and what it offers.
type WireStatus struct {
	Running  bool      `json:"running"`
	LastRun  time.Time `json:"last_run"`
	LastErr  string    `json:"last_err,omitempty"`
	Finished bool      `json:"finished"`
	// Version is the assignment the node has.
	Version int64 `json:"version"`
	// Which maintenance jobs and settings the node has.
	Backups  bool `json:"backups"`
	Restores bool `json:"restores"`
	Updates  bool `json:"updates"`
	Settings bool `json:"settings"`
	// Addrs are its home-network addresses, and Links when each of its
	// direct links last connected (Unix time, 0 never), by the other
	// server's public key (SPEC.md §10.10).
	Addrs []string         `json:"addrs,omitempty"`
	Links map[string]int64 `json:"links,omitempty"`
	// Arch is its processor (GOARCH), for placing services (Processor).
	Arch string `json:"arch,omitempty"`
}

func (w WireStatus) apply() ApplyStatus {
	st := ApplyStatus{Running: w.Running, LastRun: w.LastRun, Finished: w.Finished}
	if w.LastErr != "" {
		st.LastErr = errors.New(w.LastErr)
	}
	return st
}

// Error kinds, so the other side can rebuild typed errors.
const (
	kindNotFound = "not_found"
	kindForm     = "form"
	kindAction   = "action"
)

// wireError rebuilds a typed error from a remote one.
func wireError(err error) error {
	var re *labnet.RemoteError
	if !errors.As(err, &re) {
		return err
	}
	switch re.Kind {
	case kindNotFound:
		return ErrNotFound
	case kindForm:
		return &FormError{Msg: re.Msg}
	case kindAction:
		return &ActionError{Title: re.Title, Output: re.Output, Err: errors.New(re.Msg)}
	}
	return err
}
