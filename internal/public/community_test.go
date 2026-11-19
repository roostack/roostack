package public

import (
	"slices"
	"testing"
)

// The community's bans, from cscli's CSV as seen on a real lab
// (2026-11-19): the central API's and console lists', addresses and
// ranges, each once; not the lab's own, nor made by hand, nor simulated.
func TestParseCommunity(t *testing.T) {
	out := []byte(`id,source,ip,reason,action,country,as,events_count,expiration,simulated,alert_id
579396,CAPI,Ip:185.220.101.0,http:crawl,ban,,,0,162h42m35s,false,161
579397,CAPI,Ip:185.220.101.100,http:crawl,ban,,,0,161h42m35s,false,161
579398,CAPI,Ip:185.220.101.100,ssh:bruteforce,ban,,,0,161h42m35s,false,161
579399,lists,Range:198.51.100.0/24,firehol,ban,,,0,23h,false,162
579400,crowdsec,Ip:203.0.113.9,crowdsecurity/http-probing,ban,DE,,12,3h59m,false,170
579401,cscli,Ip:203.0.113.10,manual,ban,,,1,3h59m,false,171
579402,CAPI,Ip:192.0.2.1,http:scan,captcha,,,0,10h,false,161
579403,CAPI,Ip:192.0.2.2,http:scan,ban,,,0,10h,true,161
`)
	got, err := parseCommunity(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"185.220.101.0", "185.220.101.100", "198.51.100.0/24"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, err := parseCommunity(nil); err != nil || got != nil {
		t.Errorf("no output: %q, %v", got, err)
	}
	if _, err := parseCommunity([]byte("id,origin\n1,CAPI\n")); err == nil {
		t.Error("columns it doesn't know: no error")
	}
}
