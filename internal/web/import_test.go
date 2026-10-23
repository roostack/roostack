package web

import (
	"context"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// fakeImports is a server bringing data into Immich: its state, and the
// plan worked out by the real matcher over a few files.
type fakeImports struct {
	st       node.ImportState
	switches []string
}

func (f *fakeImports) Import(_ context.Context, service string) (node.ImportView, error) {
	p, _ := pkgspec.Load(pkgtest.File("immich"))
	files := fstest.MapFS{"library/upload/a.jpg": {}, "library/thumbs/a.webp": {}, "library/encoded-video/a.mp4": {}, "main.dump": {}}
	v := node.ImportView{ImportState: f.st, Mounted: f.st.Source != nil}
	v.Plan = importer.Match(p, f.st.Setup, f.st.Prefixes, f.st.Choices, files, nil)
	return v, nil
}
func (f *fakeImports) SetImportSource(_ context.Context, _ string, src node.ImportSource) error {
	if src.Library.Share != nil && src.Library.Share.Password != "" {
		src.Library.Share.Password = ""
	}
	f.st.Source = &src
	return nil
}
func (f *fakeImports) SetImportSetup(_ context.Context, _ string, s *importer.Setup) error {
	f.st.Setup, f.st.SetupDone = s, s == nil
	if s != nil {
		f.st.Prefixes, f.st.Guessed = []importer.Prefix{{Old: "/opt/immich", Now: "."}}, true
	}
	return nil
}
func (f *fakeImports) SetImportPrefixes(_ context.Context, _ string, p []importer.Prefix) error {
	f.st.Prefixes, f.st.Guessed, f.st.SetupDone = p, false, true
	return nil
}
func (f *fakeImports) SetImportChoices(_ context.Context, _ string, ch importer.Choices) error {
	f.st.Choices = ch
	return nil
}
func (f *fakeImports) BrowseImport(_ context.Context, _, rel string) (node.ImportListing, error) {
	if rel == "." {
		return node.ImportListing{Path: ".", Folders: []node.ImportEntry{{Name: "immich"}}}, nil
	}
	return node.ImportListing{Path: rel, Folders: []node.ImportEntry{{Name: "library"}}, Files: []node.ImportEntry{{Name: "main.dump", Size: 19e6}}}, nil
}
func (f *fakeImports) CheckImport(context.Context, string) error {
	f.st.Check = &importer.Report{At: time.Now(), Files: 3, Bytes: 15, Items: []importer.Item{
		{Level: importer.LevelOK, Text: "Your photos and videos: 1 file, 5 B.", More: "All readable."},
		{Level: importer.LevelWarn, Text: "2 settings the old immich_server had that Roostack doesn't set:", List: []string{"IMMICH_LOG_LEVEL=verbose"}}}}
	return nil
}
func (f *fakeImports) CopyImport(context.Context, string) error {
	f.st.Copy = &node.ImportCopy{Started: time.Now(), Finished: time.Now(), Files: 3, Bytes: 15, Passes: 1,
		Progress: importer.Progress{Files: 3, Bytes: 15, Copied: 15}}
	return nil
}
func (f *fakeImports) StopImportCopy(context.Context, string) error { return nil }
func (f *fakeImports) SwitchImport(_ context.Context, _, step string) error {
	f.switches = append(f.switches, step)
	return nil
}
func (f *fakeImports) CancelImport(context.Context, string) error {
	f.st = node.ImportState{}
	return nil
}

// Bringing Immich's data in through its page (SPEC.md §8.4): the Owner
// only; each step continues to the next, done ones shrink to a line with
// Change, and the switch is the controller's.
func TestImportPage(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Log = slog.New(slog.NewTextHandler(testWriter{t}, nil))
	x := &fakeImports{}
	fm.imports = x
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "immich", Title: "Immich", Status: StatusAwaitingData, URLs: []string{"https://photos.lab.example.com"}}}, nil
	}
	h.server.Libraries = newFakeLibraries()

	// Its page says it waits for its data.
	_, body := h.get("/services/immich")
	if !strings.Contains(body, "Immich is installed and waiting for its data.") || !strings.Contains(body, `href="/services/immich/import">Bring its data in…`) ||
		!strings.Contains(body, "ready once its data is in") || !strings.Contains(body, "Waiting for its data") {
		t.Error("the service page doesn't say it's waiting for its data")
	}

	// 1: where the files are.
	code, body := h.get("/services/immich/import")
	if code != http.StatusOK || !strings.Contains(body, `id="step-1"`) || !strings.Contains(body, "Where your files are") ||
		!strings.Contains(body, `class="card step-card step-todo"`) {
		t.Fatalf("the import page: %d\n%s", code, body)
	}
	h.post("/services/immich/import/source", url.Values{"where": {"drive"}, "drive": {"1111-2222"}, "drive_name": {"MEDIA"}, "sub": {"immich"}}, h.srv.URL)
	if x.st.Source == nil || x.st.Source.Title != "MEDIA › immich" || x.st.Source.Library.Drive != "1111-2222" {
		t.Fatalf("source %+v", x.st.Source)
	}

	// 2: the old setup, pasted.
	_, body = h.get("/services/immich/import")
	if !strings.Contains(body, "MEDIA › immich") || !strings.Contains(body, `name="inspect"`) || !strings.Contains(body, "docker inspect $(docker compose ps -aq)") {
		t.Error("step 2 isn't open")
	}
	code, body = h.post("/services/immich/import/setup", url.Values{"from": {"inspect"}, "inspect": {"not json"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), "That isn't what docker inspect prints") {
		t.Errorf("a bad paste: %d", code)
	}
	data, _ := os.ReadFile("../importer/testdata/immich-inspect.json")
	h.post("/services/immich/import/setup", url.Values{"from": {"inspect"}, "inspect": {string(data)}}, h.srv.URL)
	if x.st.Setup == nil || len(x.st.Setup.Containers) != 4 || x.st.Setup.Containers[0].Env["DB_PASSWORD"] != importer.Masked {
		t.Fatalf("setup %+v", x.st.Setup)
	}
	_, body = h.get("/services/immich/import")
	for _, want := range []string{"Where are the old machine's folders now?", `name="old.0" value="/opt/immich"`, "MEDIA › immich",
		"Roostack guessed this line", "immich_postgres", `Database "main"`, "Filled in from your old setup.", "docker exec immich_postgres"} {
		if !strings.Contains(body, want) {
			t.Errorf("step 2 lacks %q", want)
		}
	}
	h.post("/services/immich/import/paths", url.Values{"old.0": {"/opt/immich"}, "now.0": {"."}}, h.srv.URL)

	// 3: what goes where, by container path.
	_, body = h.get("/services/immich/import")
	for _, want := range []string{`id="step-3"`, "MEDIA › immich › library", "without <code>encoded-video</code> and <code>thumbs</code>",
		"Smart search models", "Not brought"} {
		if !strings.Contains(body, want) {
			i := strings.Index(body, `id="step-3"`)
			t.Errorf("step 3 lacks %q:\n%s", want, body[max(i, 0):min(len(body), max(i, 0)+3000)])
		}
	}
	// A folder chosen in the picker comes back to its row, kept on Continue.
	_, body = h.get("/services/immich/import/browse?for=storage:model-cache&path=library")
	if !strings.Contains(body, `href="/services/immich/import?for=storage%3Amodel-cache&amp;path=library"`) {
		t.Error("the picker's Use link")
	}
	_, body = h.get("/services/immich/import?for=storage:model-cache&path=library")
	if !strings.Contains(body, `name="pick.model-cache" value="library"`) {
		t.Error("the folder chosen isn't on its row")
	}
	h.post("/services/immich/import/choices", url.Values{"without_shown": {"1"}, "leave.thumbs": {"1"}, "continue": {"1"}}, h.srv.URL)
	if x.st.Choices.Storage["thumbs"] != importer.Leave || x.st.Check == nil {
		t.Fatalf("choices %+v, check %v", x.st.Choices, x.st.Check)
	}

	// 4 and 5: the check's list, then the copy.
	_, body = h.get("/services/immich/import")
	for _, want := range []string{`class="check-list"`, "check-warn", "IMMICH_LOG_LEVEL=verbose", "Copy into Immich"} {
		if !strings.Contains(body, want) {
			t.Errorf("steps 4 and 5 lack %q", want)
		}
	}
	h.post("/services/immich/import/copy", nil, h.srv.URL)

	// 6: the switch, with what to do first.
	_, body = h.get("/services/immich/import")
	for _, want := range []string{`id="step-6"`, "docker compose stop immich-server immich-machine-learning", "Switch to this data",
		"Copy again", "Immich has no data yet, so nothing will be set aside."} {
		if !strings.Contains(body, want) {
			t.Errorf("step 6 lacks %q", want)
		}
	}
	code, _ = h.post("/services/immich/import/switch", nil, h.srv.URL)
	if code != http.StatusBadRequest || len(fm.switched) != 0 {
		t.Error("switched without the old one stopped")
	}
	h.post("/services/immich/import/switch", url.Values{"stopped": {"on"}}, h.srv.URL)
	if len(fm.switched) != 1 {
		t.Error("not switched")
	}

	// Done: the result is the last step.
	x.st.Switched, x.st.Kept = time.Now(), []string{"/srv/roostack/services/immich.before-import-20261021-101010"}
	_, body = h.get("/services/immich/import")
	if !strings.Contains(body, "Immich is running with your data.") || !strings.Contains(body, `class="card step-card step-result"`) {
		t.Error("the result")
	}
	if !strings.Contains(body, "unplug the drive whenever you like") || strings.Contains(body, "Stop bringing data in…") {
		t.Error("the result's drive, or the way to stop")
	}
	// A page left open while it switched is drawn again whole.
	resp, err := h.client.Get(h.srv.URL + "/services/immich/import?part=live")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Error("the finished page isn't drawn again")
	}

	// Admins can't.
	h2, fm2 := loggedIn(t, auth.RoleAdmin)
	fm2.imports = x
	if code, _ := h2.get("/services/immich/import"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("an Admin: %d", code)
	}
}
