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

// Validate refuses a line that wouldn't be read back as written: each of
// these was checked on a dev VM with Podman 5.4's Quadlet
// (docs/ASSUMPTIONS.md A12, REVIEW-2026-10-16 H2).
func TestValidate(t *testing.T) {
	tests := []struct {
		name, key, value string
		ok               bool
	}{
		{"plain", "ShmSize", "128m", true},
		{"quoted backslash inside", "Environment", `"A=b\\c"`, true},
		{"newline in the value", "Environment", "A=1\nVolume=/:/host", false},
		{"carriage return in the value", "Environment", "A=1\rVolume=/:/host", false},
		{"backslash at the end", "ShmSize", `128m\`, false},
		{"backslash then spaces", "ShmSize", "128m\\ \t", false},
		{"space after the key", "Volume ", "/var/lib/roostack:/lab", false},
		{"space before the key", " Volume", "/var/lib/roostack:/lab", false},
		{"= in the key", "Volume=/:/host", "rw", false},
		{"newline in the key", "ShmSize=1m\nVolume", "/:/host", false},
		{"dash in the key", "X-Volume", "/:/host", false},
		{"digit first", "1Volume", "/:/host", false},
		{"empty key", "", "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u Unit
			u.Section("Container").Add(tt.key, tt.value)
			if err := u.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok %v", err, tt.ok)
			}
		})
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
