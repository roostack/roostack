package osupdate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
)

// Roostack's own updates (SPEC.md §7, "A new release series"). Its apt
// source follows one release series (0.4), so patch releases install
// themselves with Debian's updates. A newer series (0.5) is published in
// a suite of its own, which this server notices from the repository's list
// of series and offers on its Updates page. Once the Owner accepts it, the
// next OS updates run, after a backup that worked, moves the source to it
// and installs it (the agent's updatesScript).

// SourceFile is Roostack's apt source; tests replace it.
var SourceFile = agent.RoostackSource

const (
	seriesKey   = "roostack.series"          // the last check (seriesCheck)
	acceptedKey = "roostack.series.accepted" // the series the Owner accepted
)

// Series is what this server knows about Roostack's release series.
type Series struct {
	// Current is the series the source follows ("0.4"); "" when there's no
	// source to follow (a development build, or the file was removed).
	Current string
	// Source is where it comes from (the source's first address).
	Source string
	// Newest and NewestVersion are the newest series the repository has,
	// when it's newer than Current ("0.5", "0.5.0").
	Newest, NewestVersion string
	// Accepted is a series the Owner accepted, not installed yet.
	Accepted string
	// Waiting says why an accepted series hasn't been installed yet (no
	// backup that worked), after a run that left it.
	Waiting string
	// Checked is the last look at the repository; Error, when it failed.
	Checked time.Time
	Error   string
}

// seriesCheck is the stored result of the last look at the repository.
type seriesCheck struct {
	Checked time.Time `json:"checked"`
	Newest  string    `json:"newest,omitempty"`
	Version string    `json:"version,omitempty"`
	Error   string    `json:"error,omitempty"`
	// Waiting is set by a run that left an accepted series for later.
	Waiting string `json:"waiting,omitempty"`
}

// source is what Roostack's apt source says: its series and the first
// address it fetches from. Disabled sources (Enabled: no, as in a
// development build) follow nothing.
type source struct {
	Suite, URI string
}

// readSource reads Roostack's deb822 source.
func readSource() (source, error) {
	data, err := os.ReadFile(SourceFile)
	if errors.Is(err, os.ErrNotExist) {
		return source{}, nil
	}
	if err != nil {
		return source{}, err
	}
	var s source
	enabled := true
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "enabled":
			enabled = v != "no"
		case "suites":
			if f := strings.Fields(v); len(f) > 0 {
				s.Suite = f[0]
			}
		case "uris":
			for _, u := range strings.Fields(v) {
				if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
					s.URI = strings.TrimSuffix(u, "/")
					break
				}
			}
		}
	}
	if !enabled || !agent.ValidSeries(s.Suite) {
		return source{}, nil
	}
	return s, nil
}

// newerSeries reports whether series a is newer than b (0.10 is newer
// than 0.9). Both must be valid series.
func newerSeries(a, b string) bool {
	am, an := splitSeries(a)
	bm, bn := splitSeries(b)
	return am > bm || am == bm && an > bn
}

func splitSeries(s string) (int, int) {
	maj, min, _ := strings.Cut(s, ".")
	m, _ := strconv.Atoi(maj)
	n, _ := strconv.Atoi(min)
	return m, n
}

// parseSeriesList reads the repository's list of series: "<series>
// <newest version>" lines, # for comments (scripts/build-apt-repo.sh).
// It returns the newest series and its newest version.
func parseSeriesList(r io.Reader) (series, version string, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !agent.ValidSeries(f[0]) || !strings.HasPrefix(f[1], f[0]+".") {
			return "", "", fmt.Errorf("unexpected line in the list of series: %q", line)
		}
		if series == "" || newerSeries(f[0], series) {
			series, version = f[0], f[1]
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if series == "" {
		return "", "", errors.New("the list of series is empty")
	}
	return series, version, nil
}

// CheckSeries looks at the repository's list of series, from the address
// in Roostack's source (so a mirror works too), and records the newest.
// The list only prompts the offer: moving to a series runs apt, which
// checks that series' signature with Roostack's key.
func (s *Service) CheckSeries(ctx context.Context) error {
	src, err := readSource()
	if err != nil || src.URI == "" {
		return err
	}
	c := s.seriesCheck(ctx)
	c.Checked, c.Error = s.Now(), ""
	newest, version, err := s.fetchSeries(ctx, src.URI+"/series")
	if err != nil {
		c.Error = err.Error()
	} else {
		c.Newest, c.Version = newest, version
	}
	if serr := s.saveSeriesCheck(ctx, c); serr != nil {
		return serr
	}
	return err
}

func (s *Service) fetchSeries(ctx context.Context, url string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	return parseSeriesList(io.LimitReader(resp.Body, 64<<10))
}

func (s *Service) seriesCheck(ctx context.Context) seriesCheck {
	var c seriesCheck
	if v, _ := s.Store.Setting(ctx, seriesKey); v != "" {
		json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (s *Service) saveSeriesCheck(ctx context.Context, c seriesCheck) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, seriesKey, string(data))
}

// Series reports this server's release series and any newer one.
func (s *Service) Series(ctx context.Context) (Series, error) {
	src, err := readSource()
	if err != nil {
		return Series{}, err
	}
	out := Series{Current: src.Suite, Source: src.URI}
	if out.Current == "" {
		return out, nil
	}
	c := s.seriesCheck(ctx)
	out.Checked, out.Error = c.Checked, c.Error
	if c.Newest != "" && newerSeries(c.Newest, out.Current) {
		out.Newest, out.NewestVersion = c.Newest, c.Version
	}
	if a, _ := s.Store.Setting(ctx, acceptedKey); a != "" && agent.ValidSeries(a) && newerSeries(a, out.Current) {
		out.Accepted, out.Waiting = a, c.Waiting
	}
	return out, nil
}

// ErrNoSuchSeries means the series offered has changed since the page
// was shown, or there's none.
var ErrNoSuchSeries = errors.New("that release series isn't on offer")

// AcceptSeries records the Owner's go-ahead for a newer series: the next
// OS updates run moves to it, after a backup that worked.
func (s *Service) AcceptSeries(ctx context.Context, series string) error {
	cur, err := s.Series(ctx)
	if err != nil {
		return err
	}
	if cur.Newest == "" || series != cur.Newest {
		return ErrNoSuchSeries
	}
	return s.Store.SetSetting(ctx, acceptedKey, series)
}

// seriesToInstall is the series this run should move to: one the Owner
// accepted, still newer than the source's, and only after a backup that
// worked (BackupCheck). Otherwise "", recording why it waits.
func (s *Service) seriesToInstall(ctx context.Context) string {
	cur, err := s.Series(ctx)
	if err != nil || cur.Accepted == "" {
		return ""
	}
	why := ""
	if s.BackupCheck != nil {
		why = s.BackupCheck(ctx)
	}
	c := s.seriesCheck(ctx)
	if c.Waiting != why {
		c.Waiting = why
		if err := s.saveSeriesCheck(ctx, c); err != nil {
			s.Log.Error("recording why Roostack's update waits", "err", err)
		}
	}
	if why != "" {
		return ""
	}
	return cur.Accepted
}

// WatchSeries checks for a new release series a few minutes after
// starting, then every few hours, until ctx ends.
func (s *Service) WatchSeries(ctx context.Context, first, every time.Duration) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		if err := s.CheckSeries(ctx); err != nil {
			s.Log.Warn("checking for a new Roostack release series", "err", err)
		}
	}
}
