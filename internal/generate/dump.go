package generate

import (
	"fmt"
	"strings"
)

// Dump renders an Output as readable text, for golden files and debugging.
func Dump(o *Output) string {
	var b strings.Builder
	for _, f := range o.Quadlets {
		fmt.Fprintf(&b, "==> quadlet/%s\n%s\n", f.Name, f.Content)
	}
	if o.CaddySite != nil {
		fmt.Fprintf(&b, "==> caddy/sites/%s\n%s\n", o.CaddySite.Name, o.CaddySite.Content)
	}
	for _, f := range o.ConfigFiles {
		what := "the whole file"
		if f.Block {
			what = "a block at the end of the app's own file"
		}
		fmt.Fprintf(&b, "==> config %s (%s: %s)\n%s\n", f.HostPath(), f.Title, what, f.Shown())
	}
	b.WriteString("==> units\n")
	for _, u := range o.Units {
		fmt.Fprintf(&b, "%s\n", u)
	}
	b.WriteString("\n==> secrets\n")
	for _, s := range o.Secrets {
		fmt.Fprintf(&b, "%s generate=%t setting=%q\n", s.Name, s.Generate, s.Setting)
	}
	b.WriteString("\n==> dirs\n")
	for _, d := range o.Dirs {
		fmt.Fprintf(&b, "%s\n", d)
	}
	b.WriteString("\n==> backup include\n")
	for _, p := range o.Backup.Include {
		fmt.Fprintf(&b, "%s\n", p)
	}
	b.WriteString("\n==> backup exclude\n")
	for _, p := range o.Backup.Exclude {
		fmt.Fprintf(&b, "%s\n", p)
	}
	b.WriteString("\n==> backup prepare\n")
	for _, s := range o.Backup.Prepare {
		// Schemas only where a database has them, as they were added later.
		fmt.Fprintf(&b, "%s\n", strings.Replace(fmt.Sprintf("%+v", s), " Schemas:[]", "", 1))
	}
	return b.String()
}
