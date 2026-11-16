package web

import (
	"fmt"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/instance"
)

// A service's extra environment variables as text (SPEC.md §8.1, "Extra
// environment variables"): one NAME=value a line, as in a .env file, so
// one can be pasted as it is.

// envHidden stands for a secret's value in the text: left there, the
// secret keeps it.
const envHidden = "‹hidden›"

// maxEnvText is the most text a container's box takes.
const maxEnvText = 64 << 10

// envLine is a variable in the text, with its line number.
type envLine struct {
	Line        int
	Name, Value string
	// Hidden: the value is envHidden, so a secret keeps its own.
	Hidden bool
}

// parseEnvText reads the text: blank lines and # comments are left out,
// "export " in front and quotes around a value are taken off. It returns
// the variables in order, and what's wrong with lines it couldn't read.
func parseEnvText(text string) ([]envLine, []string) {
	if len(text) > maxEnvText {
		return nil, []string{"The text is longer than 64 KB."}
	}
	var lines []envLine
	var problems []string
	first := map[string]int{}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok {
			problems = append(problems, fmt.Sprintf("Line %d has no = between a name and its value.", n))
			continue
		}
		if !instance.ValidEnvName(name) {
			problems = append(problems, fmt.Sprintf("Line %d: %q isn't a name an environment variable can have: letters, digits, and underscores, not starting with a digit.", n, name))
			continue
		}
		v, err := envValue(strings.TrimSpace(value))
		if err != nil {
			problems = append(problems, fmt.Sprintf("Line %d: %s", n, err))
			continue
		}
		if at, dup := first[name]; dup {
			problems = append(problems, fmt.Sprintf("Lines %d and %d both give %s.", at, n, name))
			continue
		}
		first[name] = n
		lines = append(lines, envLine{Line: n, Name: name, Value: v, Hidden: v == envHidden})
	}
	return lines, problems
}

// envValue is a value as written: in double quotes (with \" and \\ inside),
// in single quotes (as it is), or bare.
func envValue(s string) (string, error) {
	switch {
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("the double quotes around the value aren't closed as a .env file needs")
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return "", fmt.Errorf("a value can't have a line break in it")
		}
		return v, nil
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

// envText is a container's variables as text, a secret's value hidden.
func envText(vars []instance.EnvVar, container string) string {
	var b strings.Builder
	for _, e := range vars {
		if e.Container != container {
			continue
		}
		v := e.Value
		switch {
		case e.Secret:
			v = envHidden
		case v != strings.TrimSpace(v) || strings.ContainsAny(v, `"'`) || strings.HasPrefix(v, "#"):
			v = strconv.Quote(v)
		}
		fmt.Fprintf(&b, "%s=%s\n", e.Name, v)
	}
	return b.String()
}

// looksSecret says whether a variable's name suggests a secret, for the
// check before saving to tick: PASSWORD, PASSWD, SECRET, TOKEN, or KEY in
// it.
func looksSecret(name string) bool {
	up := strings.ToUpper(name)
	for _, w := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "KEY", "PASS"} {
		if strings.Contains(up, w) {
			return true
		}
	}
	return false
}
