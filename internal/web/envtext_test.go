package web

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
)

// Pasted .env text: comments and blank lines left out, export and quotes
// taken off, a secret's ‹hidden› kept, and each problem with its line.
func TestParseEnvText(t *testing.T) {
	lines, problems := parseEnvText(`# Email
export FORGEJO__mailer__ENABLED=true
FORGEJO__mailer__FROM="Smiths' \"Forgejo\" <git@smith.family>"
FORGEJO__mailer__PASSWD=‹hidden›
SINGLE='a "b" c'
URL=https://x.example/#frag

no equals
1BAD=x
FORGEJO__mailer__ENABLED=false
OPEN="unclosed
`)
	want := []envLine{{2, "FORGEJO__mailer__ENABLED", "true", false}, {3, "FORGEJO__mailer__FROM", `Smiths' "Forgejo" <git@smith.family>`, false},
		{4, "FORGEJO__mailer__PASSWD", envHidden, true}, {5, "SINGLE", `a "b" c`, false}, {6, "URL", "https://x.example/#frag", false},
		{11, "OPEN", `"unclosed`, false}}
	if len(lines) != len(want) {
		t.Fatalf("lines: %+v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, lines[i], want[i])
		}
	}
	for _, p := range []string{"Line 8 has no =", "Line 9: \"1BAD\"", "Lines 2 and 10 both give FORGEJO__mailer__ENABLED."} {
		if !strings.Contains(strings.Join(problems, "\n"), p) {
			t.Errorf("problems missing %q: %q", p, problems)
		}
	}
	if _, p := parseEnvText(strings.Repeat("x", maxEnvText+1)); len(p) != 1 {
		t.Error("over 64 KB")
	}
	// Written back, and read again the same.
	vars := []instance.EnvVar{{Container: "app", Name: "A", Value: " spaced "}, {Container: "app", Name: "B", Value: `say "hi"`},
		{Container: "app", Name: "S", Secret: true}, {Container: "db", Name: "C", Value: "other"}}
	text := envText(vars, "app")
	if text != "A=\" spaced \"\nB=\"say \\\"hi\\\"\"\nS=‹hidden›\n" {
		t.Errorf("text: %q", text)
	}
	again, problems := parseEnvText(text)
	if len(problems) != 0 || again[0].Value != " spaced " || again[1].Value != `say "hi"` || !again[2].Hidden {
		t.Errorf("read again: %+v %q", again, problems)
	}
	if !looksSecret("FORGEJO__metrics__TOKEN") || !looksSecret("smtp_password") || looksSecret("DEFAULT_THEME") {
		t.Error("looksSecret")
	}
}
