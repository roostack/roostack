package osupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
)

// seriesAgent records the series a run was asked to move to.
type seriesAgent struct {
	fakeAgent
	series *string
}

func (a seriesAgent) RunUpdates(_ context.Context, series string, w io.Writer) error {
	*a.series = series
	io.WriteString(w, "==> Installing updates\n")
	return nil
}

// withSource writes Roostack's apt source for the test.
func withSource(t *testing.T, content string) {
	t.Helper()
	SourceFile = filepath.Join(t.TempDir(), "roostack.sources")
	t.Cleanup(func() { SourceFile = "/etc/apt/sources.list.d/roostack.sources" })
	if content != "" {
		if err := os.WriteFile(SourceFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadSource(t *testing.T) {
	for content, want := range map[string]source{
		"# Roostack\nTypes: deb\nURIs: https://apt.roostack.cloud/\nSuites: 0.4\nComponents: main\n": {Suite: "0.4", URI: "https://apt.roostack.cloud"},
		"Types: deb\nURIs: mirror+file:///x http://10.0.2.2:8765\nSuites: 0.12\n":                    {Suite: "0.12", URI: "http://10.0.2.2:8765"},
		// A development build's: turned off, so it follows nothing.
		"Types: deb\nURIs: https://apt.roostack.cloud\nSuites: 0.4\nEnabled: no\n": {},
		"Types: deb\nURIs: https://apt.roostack.cloud\nSuites: dev\n":              {},
		"": {}, // no file
	} {
		withSource(t, content)
		got, err := readSource()
		if err != nil || got != want {
			t.Errorf("readSource(%q) = %+v, %v; want %+v", content, got, err, want)
		}
	}
}

func TestParseSeriesList(t *testing.T) {
	series, version, err := parseSeriesList(strings.NewReader("# comment\n0.4 0.4.3\n0.10 0.10.0\n0.9 0.9.2\n"))
	if err != nil || series != "0.10" || version != "0.10.0" {
		t.Errorf("got %q %q %v", series, version, err)
	}
	for _, bad := range []string{"", "# only\n", "0.4\n", "0.4 0.5.0\n", "x.y x.y.0\n", "0.4 0.4.1 extra\n"} {
		if _, _, err := parseSeriesList(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if !newerSeries("0.10", "0.9") || !newerSeries("1.0", "0.99") || newerSeries("0.4", "0.4") || newerSeries("0.3", "0.4") {
		t.Error("newerSeries")
	}
}

func TestNewSeries(t *testing.T) {
	list := "0.4 0.4.1\n0.5 0.5.0\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/series" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, list)
	}))
	defer srv.Close()
	withSource(t, "Types: deb\nURIs: "+srv.URL+"\nSuites: 0.4\nSigned-By: /usr/share/keyrings/roostack-archive-keyring.gpg\n")
	var ran string
	s, coord := newService(t, seriesAgent{series: &ran})
	ctx := context.Background()

	// Nothing on offer before a check, and nothing to accept.
	if ser, err := s.Series(ctx); err != nil || ser.Current != "0.4" || ser.Newest != "" || ser.Source != srv.URL {
		t.Errorf("before a check: %+v, %v", ser, err)
	}
	if err := s.AcceptSeries(ctx, "0.5"); !errors.Is(err, ErrNoSuchSeries) {
		t.Errorf("accept before a check: %v", err)
	}

	if err := s.CheckSeries(ctx); err != nil {
		t.Fatal(err)
	}
	ser, _ := s.Series(ctx)
	if ser.Newest != "0.5" || ser.NewestVersion != "0.5.0" || ser.Accepted != "" || ser.Checked.IsZero() {
		t.Errorf("after a check: %+v", ser)
	}
	// Only the series on offer can be accepted.
	if err := s.AcceptSeries(ctx, "0.6"); !errors.Is(err, ErrNoSuchSeries) {
		t.Errorf("accept 0.6: %v", err)
	}

	// Not accepted: a run stays on its series.
	s.Submit()
	if err := coord.jobs[0].Run(ctx, nil); err != nil || ran != "" {
		t.Errorf("run before accepting: %v, series %q", err, ran)
	}

	if err := s.AcceptSeries(ctx, "0.5"); err != nil {
		t.Fatal(err)
	}
	// Accepted, but the last backup didn't work: it waits, and says why.
	s.BackupCheck = func(context.Context) string { return "the last backup didn't work" }
	s.Submit()
	if err := coord.jobs[1].Run(ctx, nil); err != nil || ran != "" {
		t.Errorf("run without a backup: %v, series %q", err, ran)
	}
	if ser, _ := s.Series(ctx); ser.Accepted != "0.5" || ser.Waiting != "the last backup didn't work" {
		t.Errorf("waiting: %+v", ser)
	}
	// With a backup that worked, the run moves to it.
	s.BackupCheck = func(context.Context) string { return "" }
	s.Submit()
	if err := coord.jobs[2].Run(ctx, nil); err != nil || ran != "0.5" {
		t.Errorf("run after a backup: %v, series %q", err, ran)
	}

	// Once the source follows 0.5, nothing is offered or waiting.
	withSource(t, "Types: deb\nURIs: "+srv.URL+"\nSuites: 0.5\n")
	if ser, _ := s.Series(ctx); ser.Current != "0.5" || ser.Newest != "" || ser.Accepted != "" {
		t.Errorf("after moving: %+v", ser)
	}
	ran = "x"
	s.Submit()
	if err := coord.jobs[3].Run(ctx, nil); err != nil || ran != "" {
		t.Errorf("run after moving: %v, series %q", err, ran)
	}

	// A list that can't be read is kept as an error; the last offer stays.
	list = "not a list"
	if err := s.CheckSeries(ctx); err == nil {
		t.Error("bad list: no error")
	}
	withSource(t, "Types: deb\nURIs: "+srv.URL+"\nSuites: 0.4\n")
	if ser, _ := s.Series(ctx); ser.Error == "" || ser.Newest != "0.5" {
		t.Errorf("after a bad list: %+v", ser)
	}
}

// cutAgent cuts a run off as upgrading Roostack does: the agent restarts
// and stops sending its output, and the run carries on. Each look at the
// run returns the next of runs (the last one from then on).
type cutAgent struct {
	fakeAgent
	mu   sync.Mutex
	runs []agent.UpdatesRun
}

func (a *cutAgent) RunUpdates(_ context.Context, _ string, w io.Writer) error {
	io.WriteString(w, "==> Installing updates\nUnpacking roostack (0.4.1) over (0.4.0) ...\r\n")
	return fmt.Errorf("%w: unexpected EOF", agent.ErrUpdatesCut)
}

func (a *cutAgent) UpdatesStatus(context.Context) (agent.UpdatesRun, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run := a.runs[0]
	if len(a.runs) > 1 {
		a.runs = a.runs[1:]
	}
	return run, nil
}

// A run that upgrades Roostack carries on after the agent restarts; it's
// followed to its end and recorded, by this process or, if Roostack
// restarted too, by the next (SPEC.md §7).
func TestJobUpgradesRoostack(t *testing.T) {
	ctx := context.Background()
	out := "Packages that will be upgraded: curl roostack\nUnpacking roostack (0.4.1) over (0.4.0) ...\nSetting up roostack (0.4.1) ...\n"
	ag := &cutAgent{}
	s, coord := newService(t, ag)
	s.Activity, s.PollEvery = activity.New(), time.Millisecond
	ended := s.Now().Add(3 * time.Minute)
	ag.runs = []agent.UpdatesRun{{Running: true, Output: out[:40]}, {Ended: ended, Output: out}}
	s.Submit()
	if err := coord.jobs[0].Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	last, _ := s.Last(ctx)
	if !last.OK || last.Summary != "Roostack updated itself to 0.4.1 and restarted. Installed 2 updates: curl, roostack." ||
		!strings.Contains(last.Output, "Setting up roostack (0.4.1)") {
		t.Errorf("last = %+v", last)
	}
	if v, _ := s.Store.Setting(ctx, runningKey); v != "" || s.Pending() {
		t.Errorf("still marked running: %q, pending %v", v, s.Pending())
	}

	// Roostack restarts while following it: the run's left for the next
	// start, which records it once it ends, and says so in the log.
	ag.runs = []agent.UpdatesRun{{Running: true}}
	s.Submit()
	stop, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := coord.jobs[1].Run(stop, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping: %v", err)
	}
	if v, _ := s.Store.Setting(ctx, runningKey); v == "" {
		t.Fatal("the mark went with the process")
	}
	if last, _ := s.Last(ctx); last.Summary != "Roostack updated itself to 0.4.1 and restarted. Installed 2 updates: curl, roostack." {
		t.Errorf("a cut-off run overwrote the last result: %+v", last)
	}
	coord2 := &fakeCoord{}
	next := &Service{Agent: ag, Store: s.Store, Coord: coord2, Now: s.Now, Log: s.Log, Activity: activity.New(), PollEvery: time.Millisecond}
	ag.runs = []agent.UpdatesRun{{Running: true}, {Ended: ended, Status: 1, Output: "E: Sub-process /usr/bin/dpkg returned an error code (1)\n"}}
	next.Resume(ctx)
	if len(coord2.jobs) != 1 || coord2.jobs[0].Name != JobName || !next.Pending() {
		t.Fatalf("resume: %+v", coord2.jobs)
	}
	if err := coord2.jobs[0].Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if last, _ := next.Last(ctx); last.OK || last.Summary != "Updating didn't finish: exit status 1" ||
		!strings.Contains(last.Output, "returned an error code") {
		t.Errorf("resumed: %+v", last)
	}
	if act, _ := next.Activity.Latest(activity.OSUpdates); !slices.Contains(act.Lines, "E: Sub-process /usr/bin/dpkg returned an error code (1)") {
		t.Errorf("resumed log: %+v", act.Lines)
	}
	if next.Resume(ctx); len(coord2.jobs) != 1 {
		t.Error("resumed twice")
	}

	// The server restarted during the run (its status went with /run), or
	// the status left is an earlier run's: it didn't finish.
	for _, run := range []agent.UpdatesRun{{}, {Ended: s.Now().Add(-time.Hour), Output: out}} {
		s.setMark(ctx, &runMark{Started: s.Now()})
		ag.runs = []agent.UpdatesRun{run}
		c := &fakeCoord{}
		s.Coord = c
		s.Resume(ctx)
		c.jobs[0].Run(ctx, nil)
		if last, _ := s.Last(ctx); last.OK || !strings.Contains(last.Summary, "didn't finish: they were stopped, probably by the server restarting") {
			t.Errorf("cut off by %+v: %+v", run, last)
		}
	}
	if roostackUpgrade("Unpacking curl (8.14.1)") != "" {
		t.Error("another package counted as Roostack")
	}
}

// scripts/install.sh writes the package's source byte for byte, so dpkg
// takes it over on a fresh install without asking.
func TestInstallerSourceMatchesPackage(t *testing.T) {
	script, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile("../../packaging/apt/roostack.sources")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(script), "<<SOURCES\n")
	text, _, ok2 := strings.Cut(rest, "\nSOURCES\n")
	if !ok || !ok2 {
		t.Fatal("install.sh has no SOURCES here-document")
	}
	text = strings.NewReplacer(
		"$REPO", "https://apt.roostack.cloud",
		"$SUITE", "@SUITE@",
		"$KEYRING", "/usr/share/keyrings/roostack-archive-keyring.gpg",
	).Replace(text) + "\n"
	if text != string(pkg) {
		t.Errorf("install.sh writes\n%s\nbut the package has\n%s", text, pkg)
	}
}
