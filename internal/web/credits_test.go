package web

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/auth"
)

var updateCredits = flag.Bool("update-credits", false, "rewrite THIRD_PARTY.md")

var thirdPartyFile = filepath.Join("..", "..", "THIRD_PARTY.md")

func TestThirdPartyFile(t *testing.T) {
	var buf bytes.Buffer
	writeThirdParty(&buf)
	if *updateCredits {
		if err := os.WriteFile(thirdPartyFile, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(thirdPartyFile)
	if err != nil || !bytes.Equal(got, buf.Bytes()) {
		t.Fatal("THIRD_PARTY.md is out of date; run: go test ./internal/web -run TestThirdPartyFile -update-credits")
	}
}

// Every Go module compiled into MyLab's programs must be credited.
func TestEveryModuleCredited(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", "../../cmd/...").Output()
	if err != nil {
		t.Fatal(err)
	}
	var credited []string
	for _, c := range bundledCredits {
		credited = append(credited, strings.Fields(c.Module)...)
	}
	for _, m := range strings.Fields(string(out)) {
		if m != "github.com/mylab/mylab" && !slices.Contains(credited, m) {
			t.Errorf("module %s is compiled into MyLab but not credited in credits.go", m)
		}
	}
}

func TestAboutPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	h.server.Version = "1.2.3"
	_, body := h.get("/about")
	for _, want := range []string{"1.2.3", "htmx", "0BSD", "https://catppuccin.com", "Programs MyLab uses", "Apache 2.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("about page missing %q", want)
		}
	}
	if _, acct := h.get("/account"); !strings.Contains(acct, `from <a href="https://github.com/morhetz/gruvbox"`) {
		t.Error("each open-source theme should link to its source")
	}
	if _, home := h.get("/"); !strings.Contains(home, `href="/about"`) {
		t.Error("footer should link to About")
	}
}
