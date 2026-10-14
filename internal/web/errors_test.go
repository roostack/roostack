package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
)

// A browser opening a page gets the error's page, with its status; a part
// of a page (htmx) keeps the plain answer.
func TestErrorPages(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	code, body := h.get("/services/nope")
	if code != http.StatusNotFound || !strings.Contains(body, "This page has flown the coop.") || !strings.Contains(body, `class="es-scene"`) ||
		!strings.Contains(body, `href="/"`) || strings.Contains(body, "404 page not found") {
		t.Errorf("404: %d\n%s", code, body)
	}
	m, _ := loggedIn(t, auth.RoleMember)
	if code, body := m.get("/settings/updates"); code != http.StatusForbidden || !strings.Contains(body, "above your pecking order.") {
		t.Errorf("403: %d\n%s", code, body)
	}
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/services/nope", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || strings.Contains(string(b), "es-scene") {
		t.Errorf("htmx 404: %d %s", resp.StatusCode, b)
	}
}
