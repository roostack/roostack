package attention

import (
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// CloudSpace is Roostack Cloud backup's space, as Cloud last said
// (asked at each check-in): the account's quota, which its labs share
// (Cloud's DESIGN §6(b)), what they take of it together (Used), and this
// lab's share (Lab). Shared says the account's other labs take some too;
// a Cloud from before it (2026-10-19) gives only this lab's, which is
// then all Used says.
type CloudSpace struct {
	Used, Lab, Quota int64
	Percent          int64
	Shared           bool
}

// CloudSpaceOf is Cloud backup's space, if the lab may use it (nil
// otherwise, or until Cloud has said).
func CloudSpaceOf(st cloud.Status) *CloudSpace {
	if !st.Linked || st.Storage == nil || st.Storage.QuotaBytes <= 0 {
		return nil
	}
	u := st.Storage
	c := &CloudSpace{Used: max(u.AccountUsedBytes, u.UsedBytes), Lab: u.UsedBytes, Quota: u.QuotaBytes}
	c.Shared, c.Percent = c.Used > c.Lab, c.Used*100/c.Quota
	return c
}

// cloudSpaceWarn is the share of the quota from which Cloud emails the
// account, and the lab says so too: 80%, then 100% (Cloud's DESIGN §6(b)).
const cloudSpaceWarn = 80

// Warning is what Home and the backup's card say once the account's
// labs take 80% of the quota or more ("" below that, or for nil): the
// same moments Cloud emails the account.
func (c *CloudSpace) Warning() string {
	if c == nil || c.Percent < cloudSpaceWarn {
		return ""
	}
	took := fmt.Sprintf("this lab's backups there take %s of the %s in your plan", units.Bytes(c.Used), units.Bytes(c.Quota))
	if c.Shared {
		took = fmt.Sprintf("your account's labs take %s of the %s in your plan, this lab's backups %s",
			units.Bytes(c.Used), units.Bytes(c.Quota), units.Bytes(c.Lab))
	}
	if c.Percent >= 100 {
		return "Roostack Cloud backup is full: " + took + ". Roostack Cloud will be in touch about more space."
	}
	return fmt.Sprintf("Roostack Cloud backup is %d%% full: %s.", c.Percent, took)
}

// CloudUnreachableFor is how long Cloud can't be reached before the home
// page says so (checks are daily; one missed isn't worth a word).
const CloudUnreachableFor = 3 * 24 * time.Hour

// cloudStanding adds what Roostack Cloud has on hold to Needs attention:
// the account suspended, names suspended, refused or waiting for
// approval, and the relay suspended or turned off in Cloud (as Cloud last
// said: its check-in, and the relay's answers).
func (lab *Lab) cloudStanding(l *List) {
	st := lab.Cloud.Status()
	if !st.Linked {
		return
	}
	// Cloud backup's space, at the levels Cloud emails about.
	if text := CloudSpaceOf(st).Warning(); text != "" {
		l.add(Item{Key: "cloud-space", Level: Warning, Text: text, Link: "/backups#plan", LinkText: "Backups"})
	}
	if st.Suspended != "" {
		l.add(Item{Key: "cloud-suspended", Level: Problem,
			Text: "Roostack Cloud has suspended this lab's account: " + strings.TrimSuffix(st.Suspended, ".") +
				". Its names and relay won't work until it's lifted.",
			Link: "/settings/cloud", LinkText: "Roostack Cloud"})
	}
	for _, n := range st.Names {
		switch n.State {
		case cloudapi.NameSuspended:
			l.add(Item{Key: "cloud-name-suspended:" + n.Host, Level: Problem,
				Text: n.Host + " is suspended by Roostack Cloud: " + strings.TrimSuffix(n.Reason, ".") + ".",
				Link: "/settings/cloud#names", LinkText: "Roostack Cloud"})
		case cloudapi.NameRefused:
			l.add(Item{Key: "cloud-name-refused:" + n.Host, Level: Problem,
				Text: n.Host + " wasn't approved by Roostack Cloud, so it won't work: choose another name.",
				Link: "/settings/cloud#names", LinkText: "Why"})
		case cloudapi.NameWaiting:
			l.add(Item{Key: "cloud-name-waiting:" + n.Host, Level: Info,
				Text: n.Host + " is waiting for Roostack Cloud's approval: it will work once it's approved.",
				Link: "/settings/cloud#names", LinkText: "Why"})
		}
	}
	if !lab.Manager.RelayOn() {
		return
	}
	switch off, suspended := lab.Cloud.RelayStanding(); {
	case suspended != "":
		l.add(Item{Key: "cloud-relay-suspended", Level: Problem,
			Text: "Roostack Cloud has suspended this lab's relay: " + strings.TrimSuffix(suspended, ".") +
				". Public services aren't coming through it.",
			Link: "/settings/public#way-in", LinkText: "Public access"})
	case !off.IsZero():
		l.add(Item{Key: "cloud-relay-off", Level: Warning,
			Text: "Public services aren't coming through Roostack Cloud's relay: it was turned off in Roostack Cloud.",
			Link: "/settings/public#way-in", LinkText: "Turn it on"})
	}
	// A pause placed from Cloud's website (SPEC.md §6.16): information, as
	// a person chose it, like a pause placed here.
	if at := lab.Cloud.RelayPaused(); !at.IsZero() {
		l.add(Item{Key: "cloud-relay-paused", Level: Info,
			Text: "Public access is paused at Roostack Cloud's relay, since " + words.When(at) + ", from Roostack Cloud's website. " +
				"Visitors coming through the relay are getting a connection error, and everything is working at home as usual.",
			Link: "/settings/public#way-in", LinkText: "Public access"})
	}
}

// cloudRefused adds the services kept private because Roostack Cloud
// doesn't allow their public access through roost.ac (SPEC.md §6.11): a
// warning, since the Owner made them public, and the way out is theirs
// (asking for approval, or their own domain).
func (lab *Lab) cloudRefused(l *List) {
	if lab.CloudRefusals == nil {
		return
	}
	for _, c := range lab.CloudRefusals() {
		item := Item{Key: "cloud-public:" + c.Name, Level: Warning, LinkText: "Why",
			Link: "/services/" + c.Name + "/public/" + c.Route}
		host := strings.Join(c.Hosts, " and ")
		switch c.State {
		case cloudapi.NameOwnPackage:
			item.Text = c.Title + " is staying private: Roostack Cloud only allows public access through free roost.ac names for services from the registry, and " +
				c.Title + " is from one of your own packages. Roostack will make " + host + " public by itself once your account is approved, or you can use your own domain."
		case cloudapi.NameNeedsApproval:
			item.Text = c.Title + " is staying private: Roostack Cloud only allows public access for it through roost.ac for approved accounts. " +
				"Roostack will make " + host + " public by itself once your account is approved."
		default:
			item.Level = Info
			item.Text = "Roostack is asking Roostack Cloud whether it allows public access for " + c.Title + ". " +
				host + " is staying private until Roostack Cloud answers."
		}
		l.add(item)
	}
}

// relayDomain is the lab's own domain's proof, while relayed ("" when
// its address is a roost.ac name).
func (lab *Lab) relayDomain() *cloudapi.RelayDomain {
	if lab.Cloud == nil || !lab.Manager.RelayOn() || lab.Cloud.Status().Address == lab.Domain {
		return nil
	}
	_, d := lab.Cloud.RelayName()
	return d
}
