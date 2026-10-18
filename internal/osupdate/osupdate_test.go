package osupdate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/maintenance"
)

func TestSummarize(t *testing.T) {
	for out, want := range map[string]string{
		"Starting unattended upgrades script\nNo packages found that can be upgraded unattended and no pending auto-removals\n": "Everything was already up to date.",
		"Allowed origins are: ...\nPackages that will be upgraded: libc6 linux-image-amd64 openssl\nWriting dpkg log\n":         "Installed 3 updates: libc6, linux-image-amd64, openssl.",
		"INFO Packages that will be upgraded: openssl\n":                                                                        "Installed 1 update: openssl.",
		"something else\n": "Updates ran.",
	} {
		if got := Summarize(out); got != want {
			t.Errorf("Summarize(%q) = %q, want %q", out, got, want)
		}
	}
}

type fakeAgent struct {
	out string
	err error
}

func (f fakeAgent) RunUpdates(_ context.Context, _ string, w io.Writer) error {
	io.WriteString(w, f.out)
	return f.err
}
func (f fakeAgent) UpdatesStatus(context.Context) (agent.UpdatesRun, error) {
	return agent.UpdatesRun{}, nil
}
func (f fakeAgent) Reboot(context.Context) error { return nil }
func (f fakeAgent) HostChanges(context.Context) (agent.HostChanges, error) {
	return agent.HostChanges{AptSources: []string{"docker.list: https://download.docker.com/linux/debian"}}, nil
}

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKV) Setting(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}
func (k *memKV) SetSetting(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}

type fakeCoord struct {
	jobs    []maintenance.Job
	reboots int
	running *maintenance.Running
}

func (c *fakeCoord) Submit(chain ...maintenance.Job) { c.jobs = append(c.jobs, chain...) }
func (c *fakeCoord) RequestReboot()                  { c.reboots++ }
func (c *fakeCoord) Status() maintenance.Status      { return maintenance.Status{Running: c.running} }

func newService(t *testing.T, a Agent) (*Service, *fakeCoord) {
	dir := t.TempDir()
	RebootRequiredFile, RebootPkgsFile = filepath.Join(dir, "reboot-required"), filepath.Join(dir, "reboot-required.pkgs")
	t.Cleanup(func() {
		RebootRequiredFile, RebootPkgsFile, ProcStat = "/run/reboot-required", "/run/reboot-required.pkgs", "/proc/stat"
	})
	coord := &fakeCoord{}
	return &Service{Agent: a, Store: &memKV{m: map[string]string{}}, Coord: coord,
		Now: func() time.Time { return time.Date(2026, 9, 27, 2, 30, 0, 0, time.UTC) },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, coord
}

func TestJobRequestsReboot(t *testing.T) {
	s, coord := newService(t, fakeAgent{out: "==> Installing updates\nPackages that will be upgraded: linux-image-amd64\n"})
	s.Activity = activity.New()
	ctx := context.Background()
	if err := s.Submit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(); !errors.Is(err, ErrAlreadyQueued) {
		t.Errorf("second submit: %v", err)
	}
	// The kernel's postinst hook asks for a reboot.
	os.WriteFile(RebootRequiredFile, []byte("*** System restart required ***\n"), 0o644)
	os.WriteFile(RebootPkgsFile, []byte("linux-image-6.12.108+deb13-cloud-amd64\nlinux-image-6.12.108+deb13-cloud-amd64\n"), 0o644)
	if err := coord.jobs[0].Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if coord.reboots != 1 || s.Pending() {
		t.Errorf("reboots=%d pending=%v", coord.reboots, s.Pending())
	}
	last, err := s.Last(ctx)
	if err != nil || !last.OK || last.Summary != "Installed 1 update: linux-image-amd64." {
		t.Errorf("last = %+v, %v", last, err)
	}
	act, _ := s.Activity.Latest(activity.OSUpdates)
	if act.Running() || act.Status != "Installed 1 update: linux-image-amd64." ||
		!slices.Contains(act.Lines, "Packages that will be upgraded: linux-image-amd64") ||
		!slices.Contains(act.Lines, "A restart is needed to finish installing updates.") {
		t.Errorf("activity = %+v", act)
	}
	ProcStat = filepath.Join(filepath.Dir(RebootRequiredFile), "stat")
	os.WriteFile(ProcStat, []byte("cpu  1 2 3\nbtime 1790467008\nprocesses 42\n"), 0o644)
	if got := LastBoot(); !got.Equal(time.Unix(1790467008, 0)) {
		t.Errorf("LastBoot = %v", got)
	}
	if rs := s.RestartStatus(); !rs.Needed || rs.Since.IsZero() || rs.LastBoot.IsZero() {
		t.Errorf("restart status = %+v", rs)
	}
	if need, pkgs := RebootNeeded(); !need || !slices.Equal(pkgs, []string{"linux-image-6.12.108+deb13-cloud-amd64"}) {
		t.Errorf("RebootNeeded = %v %v", need, pkgs)
	}
}

func TestJobFailure(t *testing.T) {
	s, coord := newService(t, fakeAgent{out: "E: Could not get lock", err: errors.New("exit status 100")})
	s.Submit()
	if err := coord.jobs[0].Run(context.Background(), nil); err == nil {
		t.Fatal("want the error")
	}
	last, _ := s.Last(context.Background())
	if last.OK || !strings.Contains(last.Summary, "exit status 100") || coord.reboots != 0 {
		t.Errorf("last = %+v, reboots %d", last, coord.reboots)
	}
}

func TestRestartNow(t *testing.T) {
	s, coord := newService(t, fakeAgent{})
	coord.running = &maintenance.Running{Name: "backup"}
	var busy ErrBusy
	if err := s.RestartNow(context.Background()); !errors.As(err, &busy) || busy.Job != "backup" {
		t.Errorf("restart during a backup: %v", err)
	}
	coord.running = nil
	if err := s.RestartNow(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestHostChanges(t *testing.T) {
	s, _ := newService(t, fakeAgent{})
	ctx := context.Background()
	c, err := s.HostChanges(ctx)
	if err != nil || len(c.AptSources) != 1 || c.Known {
		t.Fatalf("changes = %+v, %v", c, err)
	}
	if err := s.AcknowledgeHostChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.HostChanges(ctx); !c.Known {
		t.Error("the Owner said they know")
	}
}
