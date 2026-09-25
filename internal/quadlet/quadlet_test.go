package quadlet

import "testing"

func TestRender(t *testing.T) {
	var u Unit
	u.Section("Unit").Add("Description", "Test 100%")
	c := u.Section("Container")
	c.Add("Image", "example/app:1")
	c.Env("SIMPLE", "value")
	c.Env("SPACED", `a "b" c`)
	c.Exec([]string{"serve", "--name", "two words"})
	u.Section("Unit").Add("After", "x.service") // appends to the existing section

	want := Header + `
[Unit]
Description=Test 100%%
After=x.service

[Container]
Image=example/app:1
Environment=SIMPLE=value
Environment="SPACED=a \"b\" c"
Exec=serve --name "two words"
`
	if got := u.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestValidateNewline(t *testing.T) {
	var u Unit
	u.Section("Container").Env("BAD", "line1\nline2")
	if err := u.Validate(); err == nil {
		t.Error("expected an error for a newline in a value")
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":    "plain",
		"":         `""`,
		"a b":      `"a b"`,
		`back\sl`:  `"back\\sl"`,
		"semi;col": `"semi;col"`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
