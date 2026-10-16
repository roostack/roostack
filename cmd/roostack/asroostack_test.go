package main

import (
	"strings"
	"testing"
)

// Output from a command re-run as the roostack user reaches root's
// terminal without escape sequences, however it's split into writes.
func TestControlFilter(t *testing.T) {
	in := "Temporary password: abc\x1b]0;title\x07\r\n\tBacking up: 50%\rné \u009b31m\x7fend\x00\n"
	want := "Temporary password: abc]0;title\r\n\tBacking up: 50%\rné 31mend\n"
	for _, size := range []int{1, 2, 3, len(in)} {
		var out strings.Builder
		f := &controlFilter{w: &out}
		for i := 0; i < len(in); i += size {
			f.Write([]byte(in[i:min(i+size, len(in))]))
		}
		if out.String() != want {
			t.Errorf("writes of %d: got %q, want %q", size, out.String(), want)
		}
	}
}
