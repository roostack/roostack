package attention

import (
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/vitals"
)

func TestVitalsText(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	v := node.Vitals{Temp: 95, TempHigh: 90, TempName: "coretemp Package id 0", Services: []node.ServiceUse{{Service: "immich", Memory: 2 << 30}}}
	for kind, want := range map[string]string{
		vitals.LowMemory: "nuc is low on memory: it has had less than 10% of its memory free for 20 minutes, so its services may slow down, or the system may stop one to free some. Immich is using the most (2.1 GB).",
		vitals.Swapping:  "nuc is swapping heavily: it has been reading memory back from its disk for 20 minutes",
		vitals.Hot:       "nuc is too hot: coretemp Package id 0 is at 95 °C, at or above its high mark of 90 °C, for 20 minutes.",
	} {
		got := VitalsText(vitals.Problem{Name: "nuc", Kind: kind, Since: now.Add(-20 * time.Minute), Reading: v}, map[string]string{"immich": "Immich"}, now)
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q", kind, got)
		}
	}
}
