package attention

import (
	"testing"

	"roostack.cloud/roostack/internal/public"
)

// The weekly summary's line, by service, most first.
func TestWAFSummary(t *testing.T) {
	blocks := []public.Block{{Host: "vault.example.com"}, {Host: "photos.example.com"}, {Host: "photos.example.com"}, {Host: "old.example.com"}}
	got := wafSummary(blocks, map[string]string{"vault.example.com": "Vaultwarden", "photos.example.com": "Immich"})
	if want := "refused 4 requests that tried a known exploit (Immich 2, Vaultwarden 1, old.example.com 1)."; got != want {
		t.Errorf("got %q", got)
	}
	if wafSummary(nil, nil) != "" {
		t.Error("none")
	}
}
