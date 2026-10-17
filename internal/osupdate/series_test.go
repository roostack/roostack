package osupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A run that upgrades Roostack is cut off when Roostack restarts; that's
// not a failure (SPEC.md §7).
func TestJobUpgradesRoostack(t *testing.T) {
	s, coord := newService(t, fakeAgent{out: "Unpacking roostack (0.4.1) over (0.4.0) ...\r\nSetting up roostack (0.4.1) ...\r\n",
		err: errors.New("signal: terminated")})
	s.Submit()
	if err := coord.jobs[0].Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	last, _ := s.Last(context.Background())
	if !last.OK || !strings.Contains(last.Summary, "Roostack updated itself to 0.4.1 and restarted") {
		t.Errorf("last = %+v", last)
	}
	if roostackUpgrade("Unpacking curl (8.14.1)") != "" {
		t.Error("another package counted as Roostack")
	}
}
