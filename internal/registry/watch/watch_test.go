package watch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/registry"
)

func TestInRange(t *testing.T) {
	for _, c := range []struct {
		v, r string
		in   bool
	}{
		{"1.34.3", "< 1.34.4", true},
		{"1.34.4", "< 1.34.4", false},
		{"1.36.5", ">1.36.0, < 1.37.0", true},
		{"1.36.0", ">1.36.0, < 1.37.0", false},
		{"v2.31.1", ">= 2.0.0, <= 2.31.1", true},
		{"1.0.1", "= 1.0.1", true},
		{"1.0.1", "1.0.2", false},
		{"1.10.0", "< 1.9.0", false},
		{"1.0.0", "", false},
		{"1.0.0", ">= ", false},
	} {
		if got := InRange(c.v, c.r); got != c.in {
			t.Errorf("InRange(%q, %q) = %v", c.v, c.r, got)
		}
	}
}

func TestSuggestedSeverity(t *testing.T) {
	for _, c := range []struct {
		s    Signals
		want string
	}{
		{Signals{CVSS: ParseCVSS("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8)}, registry.SeverityCritical},
		{Signals{CVSS: ParseCVSS("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:N/VA:N/SC:N/SI:N/SA:N", 0)}, registry.SeverityCritical},
		{Signals{CVSS: ParseCVSS("CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H", 8.8), Severity: "high"}, registry.SeverityHigh},
		{Signals{CVSS: ParseCVSS("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N", 5.3), Severity: "medium"}, registry.SeverityLow},
		{Signals{Severity: "medium", Exploited: true}, registry.SeverityHigh},
	} {
		if got, why := SuggestedSeverity(c.s); got != c.want || why == "" {
			t.Errorf("%+v: %s (%s), want %s", c.s, got, why, c.want)
		}
	}
}

// fakeFeeds serves every source the watcher reads.
type fakeFeeds struct {
	advisories map[string]string // GitHub repository → its advisories (JSON)
	kev        []string
	vpatches   map[string]string // rule → its label
	osv        map[string][]string
	vulns      map[string]string
	asked      []string
}

func (f *fakeFeeds) serve(t *testing.T) Feeds {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked = append(f.asked, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/security-advisories")
			body, ok := f.advisories[repo]
			if !ok {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, body)
		case r.URL.Path == "/kev":
			var list struct {
				Vulnerabilities []KEVEntry `json:"vulnerabilities"`
			}
			for _, c := range f.kev {
				list.Vulnerabilities = append(list.Vulnerabilities, KEVEntry{CVE: c, Vendor: "Vaultwarden", Product: "Vaultwarden", Name: "Vaultwarden " + c})
			}
			json.NewEncoder(w).Encode(list)
		case r.URL.Path == "/epss":
			var data []map[string]string
			for _, c := range strings.Split(r.URL.Query().Get("cve"), ",") {
				data = append(data, map[string]string{"cve": c, "epss": "0.25"})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case r.URL.Path == "/hub":
			rules := map[string]any{}
			for name, label := range f.vpatches {
				rules[name] = map[string]any{"description": "Detects it.", "labels": map[string]any{"label": label,
					"classification": []string{"cve." + strings.TrimPrefix(name, "crowdsecurity/vpatch-")}}}
			}
			json.NewEncoder(w).Encode(map[string]any{"appsec-rules": rules})
		case r.URL.Path == "/v1/querybatch":
			var req struct {
				Queries []struct {
					Package struct {
						Purl string `json:"purl"`
					} `json:"package"`
				} `json:"queries"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, q := range req.Queries {
				var vulns []map[string]string
				for _, id := range f.osv[q.Package.Purl] {
					vulns = append(vulns, map[string]string{"id": id})
				}
				results = append(results, map[string]any{"vulns": vulns})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results})
		case strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			io.WriteString(w, f.vulns[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Feeds{HTTP: srv.Client(), GitHubAPI: srv.URL, OSVAPI: srv.URL, KEVURL: srv.URL + "/kev", EPSSAPI: srv.URL + "/epss", HubIndex: srv.URL + "/hub"}
}

// testRepo is a registry checkout with Vaultwarden at 1.34.3 and 1.34.4,
// and Forgejo (whose source isn't on GitHub).
func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"vaultwarden", "forgejo"} {
		if err := os.CopyFS(filepath.Join(dir, "packages", name), os.DirFS(filepath.Join(pkgtest.Dir(), name))); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "packages", "vaultwarden", "1.34.3-roostack.1.yaml"))
	newer := strings.Replace(string(data), "version: 1.34.3-roostack.1", "version: 1.34.4-roostack.1", 1)
	os.WriteFile(filepath.Join(dir, "packages", "vaultwarden", "1.34.4-roostack.1.yaml"), []byte(newer), 0o644)
	return dir
}

// ghsa is a GitHub advisory as its API lists it.
func ghsa(id, cve, severity, vector, rng, patched string, updated time.Time) map[string]any {
	a := map[string]any{"ghsa_id": id, "summary": "lets anyone read every vault", "severity": severity,
		"html_url":     "https://github.com/dani-garcia/vaultwarden/security/advisories/" + id,
		"published_at": updated, "updated_at": updated, "cvss": map[string]any{"vector_string": vector, "score": 9.8},
		"vulnerabilities": []map[string]any{{"package": map[string]string{"name": "vaultwarden"}, "vulnerable_version_range": rng, "patched_versions": patched}}}
	if cve != "" {
		a["cve_id"] = cve
	}
	return a
}

// TestWatch: a run drafts a strong match (an exact registry version,
// critical) as an advisory the registry's build accepts, with what its
// pull request says, puts weaker ones in the state for the digest, skips
// what doesn't affect the registry and what's old, notes what it can't
// read, and never drafts the same problem twice. New entries on CISA's
// list and CrowdSec's rules that name a package are weak matches, after
// the first run has learnt the lists.
func TestWatch(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	critical := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
	advs, _ := json.Marshal([]map[string]any{
		ghsa("GHSA-aaaa-bbbb-cccc", "CVE-2026-11111", "critical", critical, "< 1.34.4", "1.34.4", now.Add(-time.Hour)),
		ghsa("GHSA-dddd-eeee-ffff", "", "medium", "CVSS:3.1/AV:N/AC:L/PR:L/UI:R/S:U/C:L/I:N/A:N", "< 1.34.4", "1.34.4", now.Add(-time.Hour)),
		ghsa("GHSA-gggg-hhhh-jjjj", "", "critical", critical, "< 1.0.0", "1.0.0", now.Add(-time.Hour)),
		ghsa("GHSA-mmmm-pppp-qqqq", "", "critical", critical, "< 1.34.4", "1.34.4", now.Add(-90*24*time.Hour)),
	})
	ff := &fakeFeeds{advisories: map[string]string{"dani-garcia/vaultwarden": string(advs), "caddyserver/caddy": "[]",
		"crowdsecurity/crowdsec": "[]", "restic/restic": "[]"},
		kev: []string{"CVE-2026-11111"}, vpatches: map[string]string{"crowdsecurity/vpatch-CVE-2020-1": "Old thing - RCE"}}
	dir := testRepo(t)
	repo, err := ReadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Seen: map[string]string{}}
	w := &Watcher{Repo: repo, Feeds: ff.serve(t), State: st, Now: func() time.Time { return now }}
	res, err := w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drafts) != 1 {
		t.Fatalf("drafts: %+v", res.Drafts)
	}
	d := res.Drafts[0]
	if d.Advisory != "2026-0001" || d.Package != "vaultwarden" || strings.Join(d.Affected, " ") != "1.34.3-roostack.1" ||
		d.Fixed != "1.34.4-roostack.1" || d.Severity != registry.SeverityCritical || !d.Exploited || !d.Urgent || d.EPSS != 0.25 ||
		d.Title != "Vaultwarden: lets anyone read every vault" || d.Branch != "watch/2026-0001" {
		t.Errorf("draft: %+v", d)
	}
	for _, want := range []string{"**Urgent: critical, and being used in attacks.**", "`1.34.3-roostack.1`", "Fixed in:** `1.34.4-roostack.1`",
		"CVE-2026-11111 is on CISA's list", "25.0% chance", "Before merging, check"} {
		if !strings.Contains(d.PRBody, want) {
			t.Errorf("the pull request lacks %q:\n%s", want, d.PRBody)
		}
	}
	// The draft is an advisory the registry's build accepts.
	ix, err := registry.Build(registry.BuildOptions{Repo: dir, Out: t.TempDir(), Now: now})
	if err != nil {
		t.Fatalf("the registry's build refused the draft: %v", err)
	}
	if got := ix.Advisories; len(got) != 1 || got[0].Severity != registry.SeverityCritical || !got[0].Exploited ||
		got[0].Affects["vaultwarden"].Fixed != "1.34.4-roostack.1" || len(got[0].CVEs) != 1 {
		t.Errorf("built: %+v", got)
	}
	if len(res.Weak) != 1 || res.Weak[0].ID != "GHSA-dddd-eeee-ffff" || res.Weak[0].Weak == "" || len(st.Pending) != 1 {
		t.Errorf("weak: %+v", res.Weak)
	}
	if st.Seen["GHSA-gggg-hhhh-jjjj"] != "doesn't affect the registry's versions" || st.Seen["CVE-2026-11111"] != "drafted 2026-0001" {
		t.Errorf("seen: %v", st.Seen)
	}
	if _, ok := st.Seen["GHSA-mmmm-pppp-qqqq"]; ok {
		t.Error("an old advisory was looked at")
	}
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"Forgejo: its source (https://codeberg.org/forgejo/forgejo) isn't on GitHub", "Roostack: roostack/roostack's advisories couldn't be read",
		"No bills of materials yet"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if len(st.KEV) != 1 || len(st.VPatches) != 1 || !st.LastRun.Equal(now) {
		t.Errorf("state: %+v", st)
	}

	// The next run: nothing twice; a new CISA entry and a new CrowdSec rule
	// naming Vaultwarden are weak matches.
	repo, _ = ReadRepo(dir)
	ff.kev = append(ff.kev, "CVE-2026-22222")
	ff.vpatches["crowdsecurity/vpatch-CVE-2026-33333"] = "Vaultwarden - Account takeover"
	w = &Watcher{Repo: repo, Feeds: ff.serve(t), State: st, Now: func() time.Time { return now.Add(time.Hour) }}
	res, err = w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drafts) != 0 || len(res.Weak) != 2 {
		t.Fatalf("second run: %+v %+v", res.Drafts, res.Weak)
	}
	for _, f := range res.Weak {
		if f.Concerns != "the Vaultwarden package" {
			t.Errorf("by name: %+v", f)
		}
	}
	md := Digest(st.Pending, now)
	for _, want := range []string{"[GHSA-dddd-eeee-ffff]", "[CVE-2026-22222]", "[crowdsecurity/vpatch-CVE-2026-33333]", "being used in attacks", "Only in the digest because"} {
		if !strings.Contains(md, want) {
			t.Errorf("the digest lacks %q:\n%s", want, md)
		}
	}
	if Digest(nil, now) != "" {
		t.Error("an empty digest")
	}
}

// TestWatchBOMs: a library version in an image's bill of materials that
// OSV knows a critical problem in is an exact match for that package
// version, drafted.
func TestWatchBOMs(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	dir := testRepo(t)
	log4j := "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"
	os.MkdirAll(filepath.Join(dir, BOMDir, "vaultwarden"), 0o755)
	os.WriteFile(filepath.Join(dir, BOMDir, "vaultwarden", "1.34.3-roostack.1.json"),
		[]byte(`{"artifacts":[{"purl":"`+log4j+`"},{"purl":"pkg:golang/example.com/x@1.0.0"}]}`), 0o644)
	vuln, _ := json.Marshal(map[string]any{"id": "GHSA-jfh8-c2jp-5v3q", "summary": "Remote code injection in Log4j",
		"aliases": []string{"CVE-2021-44228"}, "published": now.Add(-time.Hour),
		"severity": []map[string]string{{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H"}},
		"affected": []map[string]any{{"ranges": []map[string]any{{"events": []map[string]string{{"introduced": "2.0"}, {"fixed": "2.15.0"}}}}}}})
	ff := &fakeFeeds{advisories: map[string]string{"dani-garcia/vaultwarden": "[]"},
		osv: map[string][]string{log4j: {"GHSA-jfh8-c2jp-5v3q"}}, vulns: map[string]string{"GHSA-jfh8-c2jp-5v3q": string(vuln)}}
	repo, err := ReadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := &Watcher{Repo: repo, Feeds: ff.serve(t), State: &State{Seen: map[string]string{}}, Now: func() time.Time { return now }}
	res, err := w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drafts) != 1 {
		t.Fatalf("drafts: %+v, weak %+v, notes %v", res.Drafts, res.Weak, res.Notes)
	}
	d := res.Drafts[0]
	if d.Title != "Vaultwarden: Remote code injection in Log4j (in log4j-core 2.14.1)" || strings.Join(d.Affected, " ") != "1.34.3-roostack.1" ||
		d.FixedUpstream != "log4j-core 2.15.0" || d.Fixed != "" || d.Link != "https://osv.dev/vulnerability/GHSA-jfh8-c2jp-5v3q" {
		t.Errorf("draft: %+v", d)
	}
	if _, err := registry.Build(registry.BuildOptions{Repo: dir, Out: t.TempDir(), Now: now}); err != nil {
		t.Errorf("the registry's build refused the draft: %v", err)
	}
}

// A GitHub advisory's patched versions read as a range, and the CVEs its
// text names are found.
func TestPatchedAndCVEs(t *testing.T) {
	for patched, want := range map[string]string{"1.37.0": ">= 1.37.0", "> 7.11.1": "> 7.11.1", ">= 2.0": ">= 2.0", "": ""} {
		if got := PatchedRange(patched); got != want {
			t.Errorf("PatchedRange(%q) = %q", patched, got)
		}
	}
	if InRange("7.11.1", PatchedRange("> 7.11.1")) || !InRange("7.12.0", PatchedRange("> 7.11.1")) {
		t.Error("> 7.11.1")
	}
	if got := CVEsIn("prior to 7.12 is vulnerable to CVE-2026-109618 & CVE-2026-109619 (CVE-2026-109618)."); strings.Join(got, " ") != "CVE-2026-109618 CVE-2026-109619" {
		t.Errorf("CVEsIn = %v", got)
	}
}
