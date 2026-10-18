package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labrestore"
)

type fakeLabRestore struct {
	mu        sync.Mutex
	src       labrestore.Source
	key       string
	started   *labrestore.Found
	usedDrive string
	// unfinished: a restore started earlier and didn't finish.
	unfinished bool
	// found and unopened, if set, are what Find finds.
	found    []labrestore.Found
	unopened []labrestore.Unopened
	// finished: the restore is done, and Roostack about to restart.
	finished bool
}

func (f *fakeLabRestore) Unfinished() bool { return f.unfinished }

func (f *fakeLabRestore) Find(_ context.Context, src labrestore.Source, key string) ([]labrestore.Found, []labrestore.Unopened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.src, f.key = src, key
	if f.found != nil {
		return f.found, f.unopened, nil
	}
	snap := backup.Snapshot{ID: "abc", Time: time.Now().Add(-time.Hour)}
	return []labrestore.Found{
			{Repository: "/mnt/backup/roostack/l/n-1", Domain: "lab.example.com", ServerName: "nuc", Controller: true, Snapshot: snap, Snapshots: 12},
			{Repository: "/mnt/backup/roostack/l/n-2", Domain: "lab.example.com", ServerName: "vps", Snapshot: snap, Snapshots: 9},
		},
		[]labrestore.Unopened{{Repository: "rest:https://u:hunter22@elsewhere/lab", Problem: "The recovery key doesn't open it."}}, nil
}

func (f *fakeLabRestore) Start(fd labrestore.Found) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = &fd
	return nil
}

func (f *fakeLabRestore) Drives(context.Context) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "system", Kind: "system", Usable: true},
		{ID: "uuid:64B1", Kind: "usb", Name: "OLD BACKUPS", Usable: true}}, nil
}
func (f *fakeLabRestore) UseDrive(_ context.Context, id string) (string, error) {
	f.usedDrive = id
	return "/mnt/roostack-old-backups", nil
}

func (f *fakeLabRestore) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started != nil && !f.finished
}

func (f *fakeLabRestore) Progress() (activity.Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := activity.Snapshot{Title: "Restoring lab.example.com", Status: "Restoring nuc's files", Lines: []string{"/var/lib/roostack"}}
	if f.finished {
		snap.Finished = time.Now()
	}
	return snap, true
}

// A restore started on the setup pages at home can't follow Roostack once
// it restarts as the restored lab (that closes them): the page says where
// to go next, once it's done or as soon as nothing answers.
func TestRestoreALabFromSetupPages(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	h.server.Setup = &fakeSetup{}
	fr := &fakeLabRestore{}
	h.server.LabRestore = fr
	ts := httptest.NewTLSServer(h.server.SetupHandler(func() {}))
	defer ts.Close()
	c := setupClient(t, "")
	do(t, c, "POST", ts.URL, "/setup/code", "", ts.URL, url.Values{"token": {"the-setup-code"}})
	do(t, c, "POST", ts.URL, "/setup/restore", "", ts.URL, url.Values{"kind": {"folder"}, "drive": {"other"}, "folder": {"/mnt/backup"},
		"recovery_key": {"the key"}})
	if code, _ := do(t, c, "POST", ts.URL, "/setup/restore/start", "", ts.URL, url.Values{"backup": {"0"}}); code != http.StatusOK || fr.started == nil {
		t.Fatalf("start: %d", code)
	}
	_, body := do(t, c, "GET", ts.URL, "/setup/restore/progress", "", "", nil)
	if !strings.Contains(body, `x-on:htmx:send-error="lost = true"`) || !strings.Contains(body, `<div x-show="lost" x-cloak>`) ||
		!strings.Contains(body, "Your lab has no address that opens from here") || !strings.Contains(body, "ssh -L 8480:127.0.0.1:8480 you@127.0.0.1") {
		t.Errorf("while restoring:\n%s", body)
	}
	// Done: the restored settings give the lab's address.
	fr.mu.Lock()
	fr.finished = true
	fr.mu.Unlock()
	h.server.FirstRunAddress = func() string { return "https://roost.lab.example.com" }
	req, _ := http.NewRequest("GET", ts.URL+"/setup/restore/progress", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if body := string(data); !strings.Contains(body, "This setup page closes as Roostack restarts as your lab.") ||
		!strings.Contains(body, `open your lab at <a href="https://roost.lab.example.com">`) || strings.Contains(body, `x-show="lost"`) ||
		!strings.Contains(body, "ssh -L 8480:127.0.0.1:8480 you@127.0.0.1") {
		t.Errorf("done:\n%s", body)
	}
}

func TestRestoreALabOnFirstRun(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	fr := &fakeLabRestore{}
	h.server.LabRestore = fr
	// Without the setup code entered first, a wrong one is refused.
	wrong := url.Values{"token": {"wrong"}, "kind": {"folder"}, "folder": {"/mnt/backup"}, "drive": {"other"}, "recovery_key": {"the key"}}
	if code, body := h.post("/setup/restore", wrong, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "setup code is wrong") {
		t.Fatalf("wrong setup code: %d", code)
	}
	h.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h.srv.URL)
	if _, body := h.get("/setup"); !strings.Contains(body, `href="/setup/restore"`) {
		t.Error("the first-run page should offer restoring a lab")
	}
	if _, body := h.get("/setup/restore"); strings.Contains(body, `name="token"`) {
		t.Error("once entered, the setup code isn't asked again")
	}
	if _, body := h.get("/setup/restore"); !strings.Contains(body, "OLD BACKUPS") || strings.Contains(body, `value="system"`) {
		t.Error("the restore page lists connected drives (not this server's own disk)")
	}
	form := url.Values{"token": {"the-setup-code"}, "kind": {"folder"}, "folder": {"/mnt/backup"}, "drive": {"other"}, "recovery_key": {"the key"}}
	form.Set("folder", "backup")
	if code, body := h.post("/setup/restore", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "full path") {
		t.Errorf("a relative folder: %d", code)
	}
	// Starting needs the browser that searched.
	if code, _ := h.post("/setup/restore/start", url.Values{"backup": {"0"}}, h.srv.URL); code != http.StatusOK || fr.Running() {
		t.Fatal("a restore can't start without a search")
	}

	form.Set("folder", "/mnt/backup")
	_, body := h.post("/setup/restore", form, h.srv.URL)
	if fr.key != "the key" || fr.src.Folder != "/mnt/backup" {
		t.Errorf("find got %+v %q", fr.src, fr.key)
	}
	if !strings.Contains(body, "nuc") || strings.Count(body, `showModal()">Restore this lab`) != 1 || strings.Contains(body, "hunter22") ||
		!strings.Contains(body, "rest:https://u@elsewhere/lab") {
		t.Errorf("found backups:\n%s", body)
	}
	if code, _ := h.post("/setup/restore/start", url.Values{"backup": {"0"}}, h.srv.URL); code != http.StatusOK || fr.started == nil ||
		fr.started.ServerName != "nuc" {
		t.Fatalf("start: %d %+v", code, fr.started)
	}
	if _, body := h.get("/setup/restore/progress"); !strings.Contains(body, "Restoring nuc&#39;s files") {
		t.Errorf("progress:\n%s", body)
	}
	// No Owner can be created meanwhile.
	owner := url.Values{"token": {"the-setup-code"}, "username": {"tyris"}, "password": {"a long password"}, "confirm": {"a long password"}}
	if code, _ := h.post("/setup", owner, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("creating an Owner during a restore: %d", code)
	}
	// Roostack restarts as the restored lab: the page moves on to logging in.
	h.server.mu.Lock()
	h.server.setupToken = ""
	h.server.mu.Unlock()
	h.server.LabRestore = nil
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/setup/restore/progress", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("HX-Redirect") != "/login" {
		t.Errorf("after the restart: %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}

	// A connected drive is mounted and searched.
	h2 := newHarness(t, "the-setup-code")
	fr2 := &fakeLabRestore{}
	h2.server.LabRestore = fr2
	h2.post("/setup/restore", url.Values{"token": {"the-setup-code"}, "kind": {"folder"}, "drive": {"uuid:64B1"}, "recovery_key": {"k"}}, h2.srv.URL)
	if fr2.usedDrive != "uuid:64B1" || fr2.src.Folder != "/mnt/roostack-old-backups" {
		t.Errorf("drive search: %q %+v", fr2.usedDrive, fr2.src)
	}
	if _, body := h2.get("/setup/restore"); strings.Contains(body, `value="cloud"`) {
		t.Error("Roostack Cloud is offered while it's turned off")
	}

	// From Roostack Cloud: the recovery key is all it takes (SPEC.md §6.9).
	h3 := newHarness(t, "the-setup-code")
	fr3 := &fakeLabRestore{}
	h3.server.LabRestore = fr3
	h3.server.Cloud = newCloud(t)
	h3.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h3.srv.URL)
	if _, body := h3.get("/setup/restore"); !strings.Contains(body, `value="cloud"`) {
		t.Error("Roostack Cloud isn't offered")
	}
	h3.post("/setup/restore", url.Values{"kind": {"cloud"}, "recovery_key": {" the key "}}, h3.srv.URL)
	if fr3.src.Kind != "cloud" || fr3.key != "the key" {
		t.Errorf("Cloud search: %+v %q", fr3.src, fr3.key)
	}
}

// From Roostack Cloud, every lab with the recovery key is shown with its
// own backups, and the person chooses which to restore (review M10). A
// controller's backup refused as forged is shown as a warning, each
// backup says whose folder it's in (review M26).
func TestRestoreChoosesACloudLab(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	h.server.Cloud = newCloud(t)
	day := func(d int) time.Time { return time.Date(2026, 3, d, 10, 0, 0, 0, time.Local) }
	linked := &labrestore.CloudLab{ID: "lab_new", Name: "Smith family", LinkedAt: day(10)}
	old := &labrestore.CloudLab{ID: "lab_old", Name: "Smith family", LinkedAt: day(1), UnlinkedAt: day(9)}
	gone := &labrestore.CloudLab{ID: "lab_older", Name: "Smiths", LinkedAt: day(2), UnlinkedAt: day(3)}
	snap := backup.Snapshot{ID: "abc", Time: day(12)}
	fr := &fakeLabRestore{
		found: []labrestore.Found{
			{Repository: "s3:https://s.test/b/labs/lab_new/n-0123456789", Name: "Smith family", ServerName: "nuc", ServerID: "n-0123456789",
				Folder: "n-0123456789", Controller: true, Snapshot: snap, Snapshots: 3, Lab: linked},
			{Repository: "s3:https://s.test/b/labs/lab_old/n-0123456789", Name: "Smith family", ServerName: "nuc", ServerID: "n-0123456789",
				Folder: "n-0123456789", Controller: true, Snapshot: snap, Snapshots: 40, Lab: old},
		},
		unopened: []labrestore.Unopened{
			{Repository: "s3:https://s.test/b/labs/lab_new/n-1111111111", Problem: "It says it's the backup of nuc", Lab: linked, Forged: true},
			{Lab: gone, Problem: "Roostack Cloud no longer keeps its backups."},
		},
	}
	h.server.LabRestore = fr
	h.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h.srv.URL)
	_, body := h.post("/setup/restore", url.Values{"kind": {"cloud"}, "recovery_key": {"the key"}}, h.srv.URL)
	for _, want := range []string{"Roostack Cloud has 3 labs with this recovery key", "Linked to Roostack Cloud since 10 March 2026",
		"9 March 2026: once it's restored, link it again", "Smiths in Roostack Cloud", "no longer keeps its backups",
		"Not offered: it says it&#39;s the backup of nuc", "in its own folder",
		// A lab linked again has two labs of one name: the headings differ.
		"<h3>Smith family in Roostack Cloud (linked now)</h3>", "<h3>Smith family in Roostack Cloud (unlinked 9 March 2026)</h3>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page doesn't say %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, `showModal()">Restore this lab`); n != 2 {
		t.Errorf("%d labs to restore, want 2", n)
	}
	// A lab with no backups left has nothing to restore and link again.
	if n := strings.Count(body, "once it's restored"); n != 1 {
		t.Errorf("%d labs told to link again once restored, want 1", n)
	}
	if i, j := strings.Index(body, "since 10 March"), strings.Index(body, "9 March 2026: once"); i < 0 || j < i {
		t.Error("the linked lab should come first")
	}
	if code, _ := h.post("/setup/restore/start", url.Values{"backup": {"1"}}, h.srv.URL); code != http.StatusOK || fr.started == nil ||
		fr.started.Lab != old {
		t.Fatalf("restoring the older lab: %d %+v", code, fr.started)
	}
}
