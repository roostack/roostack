package node

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/state"
)

// What crosses MyLab's network between the controller and a node (SPEC.md
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
}

// LabContext is the lab-wide settings a node's services need.
type LabContext struct {
	Domain string `json:"domain"`
	Email  string `json:"email,omitempty"`
}

// WireService is a service's instance and package files.
type WireService struct {
	Instance []byte `json:"instance"`
	Package  []byte `json:"package"`
}

// WireProxied is a service whose routes the receiving machine serves for
// another machine.
type WireProxied struct {
	WireService
	Addr string `json:"addr"`
}

// Encode turns an assignment into wire form, reading each package file
// from the lab's catalog.
func Encode(lab config.Lab, a Assignment, version int64) (WireAssignment, error) {
	w := WireAssignment{Version: version, Lab: LabContext{Domain: a.Lab.Domain, Email: a.Lab.Email},
		Secrets: a.Secrets, SetUp: a.SetUp}
	enc := func(s state.Service) (WireService, error) {
		in, err := s.Instance.Marshal()
		if err != nil {
			return WireService{}, err
		}
		pkg, err := os.ReadFile(lab.Paths.PackageFile(s.Package.Name))
		if err != nil {
			return WireService{}, err
		}
		return WireService{Instance: in, Package: pkg}, nil
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
	a := Assignment{Lab: w.Lab, Secrets: w.Secrets, SetUp: w.SetUp}
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
		return state.Service{Instance: in, Package: p}, nil
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
