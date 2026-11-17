package generate

import (
	"strings"
	"testing"
)

// TestSetYAMLKeys: a YAML block's keys are set in the app's own file,
// marked, merged into its mappings, and taken out again when the block no
// longer names them; a file with nothing to change is left exactly as it
// is (SPEC.md §8.1).
func TestSetYAMLKeys(t *testing.T) {
	app := `# Frigate's own config
mqtt:
  enabled: false
auth:
  enabled: true # the app's default
  roles:
    garden: [back]
cameras:
  back:
    ffmpeg:
      inputs:
        - path: rtsp://cam/1
`
	block := `# ` + yamlBlockHeader + `
auth:
  enabled: false
proxy:
  header_map:
    user: remote-user
  default_role: viewer
`
	got, ok := setYAMLKeys(app, block)
	if !ok {
		t.Fatal("not set")
	}
	for _, want := range []string{
		"# Frigate's own config\n",
		"  enabled: false # set by Roostack\n",
		"  roles:\n    garden: [back]\n",
		"proxy: # set by Roostack\n  header_map:\n    user: remote-user\n  default_role: viewer\n",
		"        - path: rtsp://cam/1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, yamlBlockHeader) {
		t.Errorf("the block's header is in the file:\n%s", got)
	}
	// Set again: nothing changes.
	if again, _ := setYAMLKeys(got, block); again != got {
		t.Errorf("not stable:\n%s\n---\n%s", got, again)
	}
	// Single sign-on off: Roostack's keys go, the app's stay.
	off, ok := setYAMLKeys(got, "# "+yamlBlockHeader+"\n")
	if !ok || strings.Contains(off, "proxy") || strings.Contains(off, YAMLMark) || strings.Contains(off, "enabled: true") ||
		!strings.Contains(off, "garden: [back]") {
		t.Errorf("off:\n%s", off)
	}
	// Nothing of Roostack's and nothing to set: the app's file as it was.
	if same, ok := setYAMLKeys(app, ""); !ok || same != app {
		t.Errorf("rewritten for nothing:\n%s", same)
	}
	for _, bad := range []string{"- a list\n", "key: [unclosed\n"} {
		if out, ok := setYAMLKeys(bad, block); ok || out != bad {
			t.Errorf("%q: set anyway", bad)
		}
	}
}
