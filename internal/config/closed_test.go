package config

import (
	"encoding/json"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// A closing of public access (SPEC.md §6.16) reads back the same from
// lab.yaml and from the assignment's JSON, which other servers get.
func TestClosedRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 14, 0, 0, time.UTC)
	want := PublicPause{Closed: &Closed{Pause: Pause{Since: at, By: "tom"}, Advisory: "2026-0009"}}
	y, err := yaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	j, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var fromY, fromJ PublicPause
	if err := yaml.Unmarshal(y, &fromY); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(j, &fromJ); err != nil {
		t.Fatal(err)
	}
	for _, got := range []PublicPause{fromY, fromJ} {
		if c := got.Closed; c == nil || !c.Since.Equal(at) || c.By != "tom" || c.Advisory != "2026-0009" || got.IsZero() || !got.Covers(nil) {
			t.Errorf("read back %+v from:\n%s\n%s", c, y, j)
		}
	}
}
