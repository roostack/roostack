package web

import (
	"context"
	"net/http"
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
}

func (f *fakeLabRestore) Find(_ context.Context, src labrestore.Source, key string) ([]labrestore.Found, []labrestore.Unopened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.src, f.key = src, key
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
	return f.started != nil
}

func (f *fakeLabRestore) Progress() (activity.Snapshot, bool) {
	return activity.Snapshot{Title: "Restoring lab.example.com", Status: "Restoring nuc's files", Lines: []string{"/var/lib/roostack"}}, true
}

func TestRestoreALabOnFirstRun(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	fr := &fakeLabRestore{}
	h.server.LabRestore = fr
	if _, body := h.get("/setup"); !strings.Contains(body, `href="/setup/restore"`) {
		t.Error("the first-run page should offer restoring a lab")
	}
	if _, body := h.get("/setup/restore"); !strings.Contains(body, "OLD BACKUPS") || strings.Contains(body, `value="system"`) {
		t.Error("the restore page lists connected drives (not this server's own disk)")
	}
	form := url.Values{"token": {"wrong"}, "kind": {"folder"}, "folder": {"/mnt/backup"}, "drive": {"other"}, "recovery_key": {"the key"}}
	if code, body := h.post("/setup/restore", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "setup code is wrong") {
		t.Fatalf("wrong setup code: %d", code)
	}
	form.Set("token", "the-setup-code")
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
}
