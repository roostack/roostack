package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/setup"
)

// fakeDomainItself keeps the Owner's choice, as the manager does.
type fakeDomainItself struct{ d config.DomainItself }

func (f *fakeDomainItself) DomainItself() config.DomainItself { return f.d }

func (f *fakeDomainItself) SetDomainItself(d config.DomainItself) error {
	if err := d.Validate(); err != nil {
		return errors.New("refused: " + err.Error())
	}
	f.d = d
	return nil
}

// What the domain itself shows (SPEC.md §5.3), on Settings → Your lab's
// address: the default, each choice saved, what's refused, and the simple
// page written as plain files with no scripts.
func TestDomainItselfCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	fd := &fakeDomainItself{}
	dir := t.TempDir()
	h.server.DomainItself, h.server.DomainPageDir = fd, dir
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "ghost", Title: "Ghost", URLs: []string{"https://blog.lab.example.com"}},
			{Name: "mealie", Title: "Mealie", URLs: []string{"https://recipes.lab.example.com"}}, {Name: "backup-only", Title: "No address"}}, nil
	}
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true}}
	if _, body := h.get("/settings/domain"); strings.Contains(body, "What lab.example.com shows") {
		t.Error("the card is shown while the address step isn't done")
	}
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	_, body := h.get("/settings/domain")
	if !strings.Contains(body, "What lab.example.com shows") || !strings.Contains(body, `value="nothing" x-model="show" checked`) ||
		!strings.Contains(body, `<option value="ghost"`) || strings.Contains(body, "No address") {
		t.Fatalf("the card:\n%s", body)
	}

	save := func(v url.Values) (int, string) { return h.post("/settings/domain/itself", v, h.srv.URL) }
	if code, _ := save(url.Values{"show": {"service"}, "service": {"ghost"}, "public": {"anyone"}}); code != http.StatusOK ||
		fd.d.Show != config.ShowService || fd.d.Service != "ghost" || !fd.d.Public {
		t.Errorf("a service: %d %+v", code, fd.d)
	}
	for _, bad := range []url.Values{{"show": {"service"}, "service": {"backup-only"}}, {"show": {"address"}, "address": {"blog.example"}},
		{"show": {"address"}, "address": {"https://lab.example.com/"}}, {"show": {"website"}},
		{"show": {"page"}, "heading": {strings.Repeat("x", 101)}}} {
		if code, body := save(bad); code != http.StatusBadRequest || !strings.Contains(body, `class="notice notice-error"`) || fd.d.Service != "ghost" {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if save(url.Values{"show": {"address"}, "address": {" https://blog.example/ "}}); fd.d.Address != "https://blog.example/" || fd.d.Public {
		t.Errorf("an address: %+v", fd.d)
	}

	// The simple page: the Owner's words and links, escaped, in the lab's
	// look, with the stylesheets beside it.
	save(url.Values{"show": {"page"}, "heading": {"  The   Smiths "}, "text": {"Hello <b>there</b>.\r\n\r\nSecond."},
		"link": {"mealie", "nope", "mealie"}})
	if fd.d.Page.Heading != "The Smiths" || len(fd.d.Page.Services) != 1 {
		t.Errorf("the page: %+v", fd.d.Page)
	}
	page0 := fd.d.Page
	page, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if p := string(page); !strings.Contains(p, "<h1 class=\"domain-heading\">The Smiths</h1>") || !strings.Contains(p, "<p>Hello &lt;b&gt;there&lt;/b&gt;.</p>") ||
		!strings.Contains(p, "<p>Second.</p>") || !strings.Contains(p, `<a href="https://recipes.lab.example.com">Mealie</a>`) || strings.Contains(p, "<script") {
		t.Errorf("index.html:\n%s", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "css", "status.css")); err != nil {
		t.Error("the stylesheet isn't beside it")
	}
	// Another choice keeps the page's words, as the form sends them.
	save(url.Values{"show": {"account"}, "heading": {page0.Heading}, "text": {page0.Text}, "link": page0.Services})
	if fd.d.Show != config.ShowAccount || fd.d.Page.Heading != "The Smiths" {
		t.Errorf("the page's words went: %+v", fd.d)
	}
}

// The card says when someone else's DNS record is in the way, and on a
// roost.ac name Your account is the default.
func TestDomainItselfCardNotes(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	fd := &fakeDomainItself{d: config.DomainItself{Show: config.ShowAccount}}
	h.server.DomainItself = fd
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateConflict, Record: "lab.example.com", Current: "203.0.113.7"}}
	if _, body := h.get("/settings/domain"); !strings.Contains(body, "pointing at 203.0.113.7") || !strings.Contains(body, `href="/dns/take-over"`) {
		t.Errorf("a record in the way:\n%s", body)
	}
	fd.d = config.DomainItself{}
	h.server.DNSProvider = config.ProviderCloud
	if _, body := h.get("/settings/domain"); !strings.Contains(body, `value="account" x-model="show" checked`) {
		t.Errorf("a roost.ac name's default:\n%s", body)
	}
}
