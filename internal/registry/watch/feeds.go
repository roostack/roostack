package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Feeds are the free sources the watcher reads (docs/RED-BUTTON-PLAN.md
// §10.1.1), each at an address tests can replace.
type Feeds struct {
	HTTP *http.Client
	// GitHubToken, if set, is sent to GitHub's API (the registry's CI has
	// one): without it, GitHub allows 60 requests an hour.
	GitHubToken string
	GitHubAPI   string
	OSVAPI      string
	KEVURL      string
	EPSSAPI     string
	HubIndex    string
}

// DefaultFeeds are the real sources.
func DefaultFeeds() Feeds {
	return Feeds{
		HTTP:      &http.Client{Timeout: time.Minute},
		GitHubAPI: "https://api.github.com",
		OSVAPI:    "https://api.osv.dev",
		KEVURL:    "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json",
		EPSSAPI:   "https://api.first.org/data/v1/epss",
		HubIndex:  "https://raw.githubusercontent.com/crowdsecurity/hub/master/.index.json",
	}
}

// get reads JSON from an address into out.
func (f Feeds) get(ctx context.Context, addr string, out any, header ...string) error {
	return f.do(ctx, http.MethodGet, addr, nil, out, header...)
}

func (f Feeds) do(ctx context.Context, method, addr string, body any, out any, header ...string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, addr, rd)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "roostack-registry-watch")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s %s: %s: %s", method, addr, resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GHSA is a GitHub security advisory, as a repository's advisories list it.
type GHSA struct {
	ID          string    `json:"ghsa_id"`
	CVE         string    `json:"cve_id"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"`
	URL         string    `json:"html_url"`
	Published   time.Time `json:"published_at"`
	Updated     time.Time `json:"updated_at"`
	Withdrawn   time.Time `json:"withdrawn_at"`
	CVSSVector  string    `json:"-"`
	CVSSScore   float64   `json:"-"`
	Vulnerables []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		Range   string `json:"vulnerable_version_range"`
		Patched string `json:"patched_versions"`
	} `json:"vulnerabilities"`
}

// RepoAdvisories are a GitHub repository's published security
// advisories, most recently updated first.
func (f Feeds) RepoAdvisories(ctx context.Context, repo string) ([]GHSA, error) {
	var raw []struct {
		GHSA
		CVSS struct {
			Vector string  `json:"vector_string"`
			Score  float64 `json:"score"`
		} `json:"cvss"`
		Severities map[string]struct {
			Vector string  `json:"vector_string"`
			Score  float64 `json:"score"`
		} `json:"cvss_severities"`
	}
	header := []string{"Accept", "application/vnd.github+json", "X-GitHub-Api-Version", "2022-11-28"}
	if f.GitHubToken != "" {
		header = append(header, "Authorization", "Bearer "+f.GitHubToken)
	}
	addr := f.GitHubAPI + "/repos/" + repo + "/security-advisories?state=published&sort=updated&direction=desc&per_page=100"
	if err := f.get(ctx, addr, &raw, header...); err != nil {
		return nil, err
	}
	out := make([]GHSA, 0, len(raw))
	for _, r := range raw {
		a := r.GHSA
		a.CVSSVector, a.CVSSScore = r.CVSS.Vector, r.CVSS.Score
		for _, k := range []string{"cvss_v4", "cvss_v3"} {
			if s, ok := r.Severities[k]; ok && s.Vector != "" && a.CVSSVector == "" {
				a.CVSSVector, a.CVSSScore = s.Vector, s.Score
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// KEVEntry is one of CISA's known exploited vulnerabilities.
type KEVEntry struct {
	CVE         string `json:"cveID"`
	Vendor      string `json:"vendorProject"`
	Product     string `json:"product"`
	Name        string `json:"vulnerabilityName"`
	Added       string `json:"dateAdded"`
	Description string `json:"shortDescription"`
}

// KEV is CISA's list of known exploited vulnerabilities, by CVE.
func (f Feeds) KEV(ctx context.Context) (map[string]KEVEntry, error) {
	var list struct {
		Vulnerabilities []KEVEntry `json:"vulnerabilities"`
	}
	if err := f.get(ctx, f.KEVURL, &list); err != nil {
		return nil, err
	}
	out := make(map[string]KEVEntry, len(list.Vulnerabilities))
	for _, v := range list.Vulnerabilities {
		out[v.CVE] = v
	}
	return out, nil
}

// EPSS is each CVE's chance of being exploited in the next 30 days,
// asked a hundred at a time.
func (f Feeds) EPSS(ctx context.Context, cves []string) (map[string]float64, error) {
	out := map[string]float64{}
	for len(cves) > 0 {
		n := min(len(cves), 100)
		var resp struct {
			Data []struct {
				CVE  string `json:"cve"`
				EPSS string `json:"epss"`
			} `json:"data"`
		}
		if err := f.get(ctx, f.EPSSAPI+"?cve="+url.QueryEscape(strings.Join(cves[:n], ",")), &resp); err != nil {
			return out, err
		}
		for _, d := range resp.Data {
			if v, err := strconv.ParseFloat(d.EPSS, 64); err == nil {
				out[d.CVE] = v
			}
		}
		cves = cves[n:]
	}
	return out, nil
}

// VPatch is one of CrowdSec's virtual-patch rules: usually written once
// an exploit is public.
type VPatch struct {
	Name, Label, Description string
	CVEs                     []string
}

// VPatches are the CrowdSec Hub's virtual-patch rules (appsec-rules named
// vpatch-*), by name.
func (f Feeds) VPatches(ctx context.Context) (map[string]VPatch, error) {
	var index struct {
		Rules map[string]struct {
			Description string `json:"description"`
			Labels      struct {
				Label          string   `json:"label"`
				Classification []string `json:"classification"`
			} `json:"labels"`
		} `json:"appsec-rules"`
	}
	if err := f.get(ctx, f.HubIndex, &index); err != nil {
		return nil, err
	}
	out := map[string]VPatch{}
	for name, r := range index.Rules {
		if !strings.Contains(name, "/vpatch-") {
			continue
		}
		v := VPatch{Name: name, Label: r.Labels.Label, Description: r.Description}
		for _, c := range r.Labels.Classification {
			if cve, ok := strings.CutPrefix(c, "cve."); ok {
				v.CVEs = append(v.CVEs, cve)
			}
		}
		out[name] = v
	}
	return out, nil
}

// OSVQuery asks OSV which known vulnerabilities affect each package URL
// with its version (pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1),
// a thousand at a time: their IDs, by package URL.
func (f Feeds) OSVQuery(ctx context.Context, purls []string) (map[string][]string, error) {
	out := map[string][]string{}
	for len(purls) > 0 {
		n := min(len(purls), 1000)
		type query struct {
			Package struct {
				Purl string `json:"purl"`
			} `json:"package"`
		}
		var req struct {
			Queries []query `json:"queries"`
		}
		for _, p := range purls[:n] {
			var q query
			q.Package.Purl = p
			req.Queries = append(req.Queries, q)
		}
		var resp struct {
			Results []struct {
				Vulns []struct {
					ID string `json:"id"`
				} `json:"vulns"`
			} `json:"results"`
		}
		if err := f.do(ctx, http.MethodPost, f.OSVAPI+"/v1/querybatch", req, &resp); err != nil {
			return out, err
		}
		for i, r := range resp.Results {
			for _, v := range r.Vulns {
				if i < n {
					out[purls[i]] = append(out[purls[i]], v.ID)
				}
			}
		}
		purls = purls[n:]
	}
	return out, nil
}

// OSVVuln is a vulnerability's record in OSV, as the watcher needs it.
type OSVVuln struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Details  string   `json:"details"`
	Aliases  []string `json:"aliases"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Affected []struct {
		Package struct {
			Name string `json:"name"`
			Purl string `json:"purl"`
		} `json:"package"`
		Ranges []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
	Published time.Time `json:"published"`
	Withdrawn time.Time `json:"withdrawn"`
}

// OSVVuln reads one vulnerability's record.
func (f Feeds) OSVVuln(ctx context.Context, id string) (*OSVVuln, error) {
	var v OSVVuln
	if err := f.get(ctx, f.OSVAPI+"/v1/vulns/"+url.PathEscape(id), &v); err != nil {
		return nil, err
	}
	return &v, nil
}
