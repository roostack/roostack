package agent

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// A USB drive with something on it is erased once it's been unplugged and
// the same drive plugged back in (under any name) within ReplugWindow,
// and the drive list says where it's got to all along; a different drive,
// or one not plugged back in time, isn't erased.
func TestReplug(t *testing.T) {
	var mu sync.Mutex
	blocks := []string{"vda", "sdb"}
	ids := map[string]string{"sdb": "Stick|123||1000", "sdc": "Stick|123||1000", "sdd": "Other|9||1000"}
	erased := make(chan string, 4)
	r := &replugWatch{
		blocks: func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(blocks) },
		identify: func(name string) (string, string) {
			return ids[name], "/dev/" + name
		},
		erase: func(w *replugWait) error { erased <- w.drive.Disk; return nil },
	}
	plug := func(names ...string) { mu.Lock(); blocks = append([]string{"vda"}, names...); mu.Unlock() }
	d := Drive{ID: "disk:/dev/sdb", Kind: "usb", Name: "Stick", Disk: "/dev/sdb", Erasable: true, Confirm: ConfirmReplug,
		Holds: []string{"hfsplus"}, ident: "Stick|123||1000"}
	now := time.Unix(1_800_000_000, 0)
	until := r.ask(LinuxHost{}, d, "ROOSTACK", now)
	if !until.Equal(now.Add(ReplugWindow)) || !r.ask(LinuxHost{}, d, "ROOSTACK", now.Add(time.Minute)).Equal(until) {
		t.Fatalf("until %v", until)
	}
	status := func(list []Drive) *EraseStatus {
		for _, x := range r.annotate(list) {
			if x.ident == d.ident {
				return x.Erase
			}
		}
		return nil
	}
	r.tick(now.Add(time.Second))
	if s := status([]Drive{d}); s == nil || s.Unplugged || !s.Until.Equal(until) {
		t.Fatalf("asked: %+v", s)
	}
	plug()
	r.tick(now.Add(2 * time.Second))
	if s := status(nil); s == nil || !s.Unplugged {
		t.Fatalf("unplugged: %+v (the drive is listed while it's out)", s)
	}
	plug("sdd") // another drive isn't it
	r.tick(now.Add(3 * time.Second))
	plug("sdd", "sdc") // the same drive, under another name
	r.tick(now.Add(4 * time.Second))
	select {
	case got := <-erased:
		if got != "/dev/sdc" {
			t.Errorf("erased %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not erased")
	}
	for range 100 {
		if s := status([]Drive{{ident: d.ident, Kind: "usb"}}); s != nil && s.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := status([]Drive{{ident: d.ident, Kind: "usb"}}); s == nil || !s.Done {
		t.Errorf("done: %+v", s)
	}

	// Not plugged back in time: expired, and nothing erased.
	r2 := &replugWatch{blocks: r.blocks, identify: r.identify, erase: func(*replugWait) error { return errors.New("no") }}
	plug("sdb")
	r2.ask(LinuxHost{}, d, "ROOSTACK", now)
	plug()
	r2.tick(now.Add(time.Second))
	plug("sdb")
	if more := r2.tick(now.Add(ReplugWindow + time.Second)); more {
		t.Error("still watching after it expired")
	}
	if s := r2.annotate([]Drive{d})[0].Erase; s == nil || !s.Expired || s.Erasing {
		t.Errorf("expired: %+v", s)
	}
}
