package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
	"roostack.cloud/roostack/internal/words"
)

// Email notifications (SPEC.md §6.13): what needs a person, or happened
// while nobody was looking. Problems and things waiting for someone come
// from "needs attention" (attention.go), as each person would see it on
// Home; security events come from the audit log. Each person chooses
// groups on their account page. One email per run of the notifier (every
// minute) gathers what's new for a person: a problem once it has lasted a
// few minutes, a daily reminder while it lasts, and "fixed" when it
// clears. Security emails go at once.

// Notification groups.
const (
	notifyProblems = "problems"
	notifyWaiting  = "waiting"
	notifySecurity = "security"
	notifyWeekly   = "weekly"
)

// notifyGroup is a group someone can choose, for the account page. Need is
// the permission it takes: Members can't act on the lab's problems, and
// lab security is the Owners'.
type notifyGroup struct {
	ID, Title, Hint string
	Need            auth.Permission
}

var notifyGroups = []notifyGroup{
	{notifyProblems, "Problems", "Backups failing, a disk running low, a service or server that stopped working, updates that failed. Sent once a problem has lasted a few minutes, then once a day while it lasts, and when it's fixed.", auth.ManageServices},
	{notifyWaiting, "Waiting for you", "Something Roostack can't finish without you, such as a restart to finish installing updates, once it has waited a day.", auth.ManageServices},
	{notifySecurity, "Security for the lab", "A new Owner or Admin, someone's password or two-factor reset, a server joining the lab, another lab starting to store backups here.", auth.ManageHost},
	{notifyWeekly, "Weekly summary", "On Monday mornings: backups, disk space, attacks blocked on your public addresses, and anything still needing attention.", auth.ManageServices},
}

// ProblemRecipients are the addresses the lab's problems are emailed to
// (SPEC.md §6.13): each person with an address who gets the Problems
// group. The lab reports them to Roostack Cloud, whose emails about the
// lab go to them too (SPEC.md §6.6).
func (s *Server) ProblemRecipients(ctx context.Context) ([]string, error) {
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for i := range users {
		u := &users[i]
		if e := strings.ToLower(u.Email); e != "" && userGroups(u)[notifyProblems] && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// groupsFor are the groups a role can choose.
func groupsFor(role string) []notifyGroup {
	var out []notifyGroup
	for _, g := range notifyGroups {
		if auth.Can(role, g.Need) {
			out = append(out, g)
		}
	}
	return out
}

// Audit actions that are security events (beside the existing ones).
const (
	auditLocked       = "2fa locked"
	auditNewDevice    = "new device"
	AuditServerJoined = "server joined"
	AuditGuestJoined  = "storage guest joined"
)

// Timing: a problem is emailed once it has lasted notifyDelay, something
// waiting once it has waited notifyWaitingDelay; reminders come every
// notifyRemind.
const (
	notifyDelay        = 10 * time.Minute
	notifyWaitingDelay = 24 * time.Hour
	notifyRemind       = 24 * time.Hour
	auditCursorKey     = "notify.audit"
	weeklyKey          = "notify.weekly."
)

// userGroups are the groups someone gets: what they chose, or their
// role's defaults (everything but the weekly summary).
func userGroups(u *store.User) map[string]bool {
	out := map[string]bool{}
	var chosen []string
	switch u.Notify {
	case "":
		chosen = []string{notifyProblems, notifyWaiting, notifySecurity}
	case "none":
	default:
		chosen = strings.Split(u.Notify, ",")
	}
	for _, g := range groupsFor(u.Role) {
		if slices.Contains(chosen, g.ID) {
			out[g.ID] = true
		}
	}
	return out
}

// RunNotifier sends notifications every minute until ctx ends.
func (s *Server) RunNotifier(ctx context.Context) {
	sleep.Every(ctx, time.Minute, func() {
		if err := s.notify(ctx, time.Now()); err != nil {
			s.Log.Error("notifications", "err", err)
		}
	})
}

// notifyMail is what one person is told in one email.
type notifyMail struct {
	security, fresh, still, fixed []string
	weekly                        string
	keys                          []string // the notices in fresh and still
}

func (m *notifyMail) empty() bool {
	return len(m.security)+len(m.fresh)+len(m.still)+len(m.fixed) == 0 && m.weekly == ""
}

// notify does one run: security events since the last run, then each
// person's problems.
func (s *Server) notify(ctx context.Context, now time.Time) error {
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return err
	}
	byName := map[string]*store.User{}
	for i := range users {
		byName[strings.ToLower(users[i].Username)] = &users[i]
	}
	mails := map[int64]*notifyMail{}
	mailFor := func(u *store.User) *notifyMail {
		if mails[u.ID] == nil {
			mails[u.ID] = &notifyMail{}
		}
		return mails[u.ID]
	}
	configured := false
	if s.Mail != nil {
		m, _ := s.Mail.Settings()
		configured = m.Configured()
	}

	// Security: audit rows since the last run (the first run starts from
	// now: the history isn't emailed).
	cursor, _ := s.Store.Setting(ctx, auditCursorKey)
	last, _ := strconv.ParseInt(cursor, 10, 64)
	if cursor == "" {
		last, _ = s.Store.LastAuditID(ctx)
	} else {
		entries, err := s.Store.AuditSince(ctx, last)
		if err != nil {
			return err
		}
		for _, e := range entries {
			last = e.ID
			own, labWide, subject := securityEvent(e)
			if own != "" {
				if u := byName[strings.ToLower(subject)]; u != nil {
					mailFor(u).security = append(mailFor(u).security, own)
				}
			}
			if labWide != "" {
				for i := range users {
					u := &users[i]
					if auth.Can(u.Role, auth.ManageHost) && userGroups(u)[notifySecurity] && !strings.EqualFold(u.Username, subject) {
						mailFor(u).security = append(mailFor(u).security, labWide)
					}
				}
			}
		}
	}
	s.Store.SetSetting(ctx, auditCursorKey, strconv.FormatInt(last, 10))
	if !configured {
		return nil // nothing can be sent; problems are followed once it can
	}

	for i := range users {
		u := &users[i]
		groups := userGroups(u)
		if u.Email == "" {
			continue
		}
		m := mailFor(u)
		if groups[notifyProblems] || groups[notifyWaiting] {
			if err := s.followProblems(ctx, u, groups, now, m); err != nil {
				s.Log.Error("notifications: following problems", "user", u.Username, "err", err)
			}
		}
		if groups[notifyWeekly] {
			m.weekly = s.weeklySummary(ctx, u, now)
		}
		if m.empty() {
			continue
		}
		subject, body := s.composeNotification(u, m)
		if err := s.Mail.Send(ctx, mail.Message{To: []string{u.Email}, Subject: subject, Body: body}); err != nil {
			s.Log.Error("notifications: sending", "user", u.Username, "err", err)
			continue
		}
		s.markSent(ctx, u, now, m)
	}
	return nil
}

// securityEvent turns an audit row into what to tell the person it's
// about (own) and the lab's Owners (labWide), and whose account it is.
func securityEvent(e store.AuditEntry) (own, labWide, subject string) {
	when := e.At.Local().Format("2 January at 15:04")
	by := "by " + e.Username
	if e.Username == "system" || strings.HasSuffix(e.Detail, "via command line") {
		by = "from the server's command line"
	}
	first := func() string { // the account an action was on: "sam (admin)" → sam
		f := strings.FieldsFunc(e.Detail, func(r rune) bool { return r == ' ' || r == ':' })
		if len(f) == 0 {
			return ""
		}
		return f[0]
	}
	switch e.Action {
	case "password changed":
		return "Your password was changed (" + when + ").", "", e.Username
	case "2fa enabled":
		return "Two-factor authentication was turned on for your account (" + when + ").", "", e.Username
	case auditNewDevice:
		return "Someone logged in to your account from a new browser or device (" + when + ", " + e.Detail + "). If it wasn't you, change your password.", "", e.Username
	case auditLocked:
		return "Several wrong two-factor codes were entered for your account, so codes are locked for a while (" + when + ", " + e.Detail + ").",
			e.Username + "'s two-factor codes were locked after several wrong codes (" + when + ").", e.Username
	case "password reset":
		n := first()
		return "Your password was reset " + by + " (" + when + "). If you didn't ask for this, tell your lab's Owner.",
			n + "'s password was reset " + by + " (" + when + ").", n
	case "2FA reset":
		n := first()
		return "Two-factor authentication was reset for your account " + by + " (" + when + "). Set it up again when you next log in.",
			"Two-factor authentication was reset for " + n + " " + by + " (" + when + ").", n
	case "user add":
		if strings.Contains(e.Detail, "(owner)") || strings.Contains(e.Detail, "(admin)") {
			return "", "A new account: " + e.Detail + ", added " + by + " (" + when + ").", ""
		}
	case "user role":
		if strings.HasSuffix(e.Detail, "owner") || strings.HasSuffix(e.Detail, "admin") {
			return "", "An account's role changed: " + e.Detail + ", " + by + " (" + when + ").", ""
		}
	case AuditServerJoined:
		return "", "A server joined the lab: " + e.Detail + " (" + when + ").", ""
	case AuditGuestJoined:
		return "", "Another lab started storing its backups here: " + e.Detail + " (" + when + ").", ""
	}
	return "", "", ""
}

// followProblems compares what needs attention for u now with what was
// seen before: new ones once they've lasted long enough, reminders, and
// the ones that cleared.
func (s *Server) followProblems(ctx context.Context, u *store.User, groups map[string]bool, now time.Time, m *notifyMail) error {
	known, err := s.Store.Notices(ctx, u.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, it := range s.attention(ctx, u) {
		if it.Level != levelProblem && it.Level != levelWarning || !groups[it.Group] {
			continue
		}
		waiting := it.Group == notifyWaiting
		key := it.Key
		seen[key] = true
		n, ok := known[key]
		if !ok {
			n = store.Notice{Key: key, FirstSeen: now}
		}
		n.GoneSince = time.Time{} // back (or never gone): not fixed
		n.Text = it.Text + s.linkLine(it.Link)
		delay := notifyDelay
		if waiting {
			delay = notifyWaitingDelay
		}
		switch {
		case n.SentAt.IsZero() && now.Sub(n.FirstSeen) >= delay:
			m.fresh, m.keys = append(m.fresh, n.Text), append(m.keys, key)
		case !n.SentAt.IsZero() && now.Sub(n.SentAt) >= notifyRemind:
			m.still = append(m.still, n.Text+"\n  (since "+n.FirstSeen.Local().Format("2 January at 15:04")+")")
			m.keys = append(m.keys, key)
		}
		if err := s.Store.SaveNotice(ctx, u.ID, n); err != nil {
			return err
		}
	}
	// Gone: "Fixed" once it has stayed gone as long as a problem must last
	// to be emailed, so a moment's gap (a server between retries) isn't
	// news. Never emailed: forgotten at once.
	for key, n := range known {
		if seen[key] {
			continue
		}
		if !n.SentAt.IsZero() {
			if n.GoneSince.IsZero() {
				n.GoneSince = now
				if err := s.Store.SaveNotice(ctx, u.ID, n); err != nil {
					return err
				}
				continue
			}
			if now.Sub(n.GoneSince) < notifyDelay {
				continue
			}
			m.fixed = append(m.fixed, words.FirstLine(n.Text))
		}
		if err := s.Store.DeleteNotice(ctx, u.ID, key); err != nil {
			return err
		}
	}
	return nil
}

// markSent records what was emailed, after it went.
func (s *Server) markSent(ctx context.Context, u *store.User, now time.Time, m *notifyMail) {
	if len(m.keys) > 0 {
		known, err := s.Store.Notices(ctx, u.ID)
		if err != nil {
			return
		}
		for _, key := range m.keys {
			if n, ok := known[key]; ok {
				n.SentAt = now
				s.Store.SaveNotice(ctx, u.ID, n)
			}
		}
	}
	if m.weekly != "" {
		s.Store.SetSetting(ctx, weeklyKey+strconv.FormatInt(u.ID, 10), strconv.FormatInt(now.Unix(), 10))
	}
}

// uiURL is Roostack's own address, for links in emails.
func (s *Server) uiURL(path string) string {
	if s.Domain == "" {
		return ""
	}
	return "https://" + s.uiHost() + path
}

func (s *Server) linkLine(path string) string {
	if u := s.uiURL(path); u != "" && path != "" {
		return "\n  " + u
	}
	return ""
}

// weeklySummary is the Monday summary, once a week from 8 am (empty when
// it isn't due).
func (s *Server) weeklySummary(ctx context.Context, u *store.User, now time.Time) string {
	local := now.Local()
	if local.Weekday() != time.Monday || local.Hour() < 8 {
		return ""
	}
	if v, _ := s.Store.Setting(ctx, weeklyKey+strconv.FormatInt(u.ID, 10)); v != "" {
		if at, err := strconv.ParseInt(v, 10, 64); err == nil && now.Sub(time.Unix(at, 0)) < 6*24*time.Hour {
			return ""
		}
	}
	var b strings.Builder
	for _, mn := range s.serverNodes() {
		fmt.Fprintf(&b, "%s:\n", mn.view.Label())
		if st, err := mn.node.Backups().Status(ctx); err == nil && st.Configured {
			if st.LastOK != nil {
				fmt.Fprintf(&b, "- Last backup: %s\n", whenText(st.LastOK.Finished))
			} else {
				b.WriteString("- No backup has finished yet\n")
			}
		} else if err == nil {
			b.WriteString("- Backups aren't set up\n")
		}
		if d := s.serverSpaces(ctx, mn.view, mn.node); d != nil {
			for _, v := range d.Disks {
				fmt.Fprintf(&b, "- %s: %s free of %s\n", v.Title, units.Bytes(v.Free), units.Bytes(v.Total))
			}
		}
		// A new release series, which only the Owner can accept (SPEC.md
		// §7.2); the server is named by the heading already.
		if up := mn.node.Updates(); up != nil && auth.Can(u.Role, auth.ManageHost) {
			if ser, err := up.Series(ctx); err == nil {
				if text := seriesText(ser, serverRef{This: true}); text != "" {
					fmt.Fprintf(&b, "- %s%s\n", text, s.linkLine(mn.view.Updates()+"#roostack"))
				}
			}
		}
	}
	// What CrowdSec saw on the public addresses (only once a service is
	// public: before that, it doesn't run).
	if s.Attacks != nil && s.Protected != nil && s.Protected(ctx) {
		// The requests AppSec refused (SPEC.md §6.10 item 4), by service.
		var refused string
		if blocks, ok := s.wafBlocks(ctx); ok {
			refused = wafSummary(blocks, s.titlesByHost(ctx))
		}
		if a, err := s.Attacks(ctx, 7*24*time.Hour); err == nil {
			b.WriteString("\nPublic addresses: ")
			switch {
			case a.Alerts == 0 && refused == "":
				b.WriteString("no attacks seen.\n")
			case a.Alerts == 0:
				b.WriteString("CrowdSec " + refused + "\n")
			default:
				fmt.Fprintf(&b, "CrowdSec saw %d %s from %d %s, and blocked %d.",
					a.Alerts, words.Plural(a.Alerts, "attack", "attacks"), a.Addresses, words.Plural(a.Addresses, "address", "addresses"), a.Blocked)
				if len(a.Kinds) > 0 {
					b.WriteString(" Most were: " + strings.Join(a.Kinds, ", ") + ".")
				}
				if refused != "" {
					b.WriteString(" It also " + refused)
				}
				b.WriteString("\n")
			}
		}
	}
	var open []string
	for _, it := range s.attention(ctx, u) {
		if it.Level == levelProblem || it.Level == levelWarning {
			open = append(open, "- "+it.Text)
		}
	}
	if len(open) == 0 {
		b.WriteString("\nNothing needs attention.\n")
	} else {
		b.WriteString("\nStill needing attention:\n" + strings.Join(open, "\n") + "\n")
	}
	return b.String()
}

// composeNotification writes one person's email.
func (s *Server) composeNotification(u *store.User, m *notifyMail) (subject, body string) {
	lab := s.labName()
	if lab == "" {
		lab = "Your lab"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hello %s,\n\n", u.Username)
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString(title + "\n\n")
		for _, l := range lines {
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("\n")
	}
	section("Security:", m.security)
	section("Needs attention:", m.fresh)
	section("Still needs attention:", m.still)
	section("Fixed:", m.fixed)
	if m.weekly != "" {
		b.WriteString("This week:\n\n" + m.weekly + "\n")
	}
	fmt.Fprintf(&b, "-- \nRoostack, for %s.", lab)
	if link := s.uiURL("/account/notifications"); link != "" {
		fmt.Fprintf(&b, "\nChoose which emails you get: %s", link)
	}
	b.WriteString("\n")

	headline := func(prefix string, lines []string) string {
		t := words.FirstLine(lines[0])
		if len(t) > 80 {
			t = t[:77] + "…"
		}
		if len(lines) > 1 {
			t += fmt.Sprintf(" (and %d more)", len(lines)-1)
		}
		return prefix + t
	}
	switch {
	case len(m.security) > 0:
		subject = headline("Security: ", m.security)
	case len(m.fresh) > 0:
		subject = headline("Needs attention: ", m.fresh)
	case len(m.still) > 0:
		subject = headline("Still needs attention: ", m.still)
	case len(m.fixed) > 0:
		subject = headline("Fixed: ", m.fixed)
	default:
		subject = "This week"
	}
	return "[" + lab + "] " + subject, b.String()
}

// deviceCookie marks a browser someone has logged in from, so a login
// from a new one can be a security email.
const deviceCookie = "roostack_device"

// noteDevice remembers the browser u just logged in from; a new one, for
// someone who has logged in before, is audited (and so emailed).
func (s *Server) noteDevice(w http.ResponseWriter, r *http.Request, u *store.User) {
	if fromSetupListener(r) {
		return // no cookie that outlives the setup pages (SPEC.md §6.3.2)
	}
	token := ""
	if c, err := r.Cookie(deviceCookie); err == nil && len(c.Value) == 32 {
		token = c.Value
	} else {
		b := make([]byte, 16)
		rand.Read(b)
		token = hex.EncodeToString(b)
	}
	sum := sha256.Sum256([]byte(token))
	isNew, hadOthers, err := s.Store.KnownDevice(r.Context(), u.ID, hex.EncodeToString(sum[:]), time.Now())
	if err != nil {
		s.Log.Error("remembering a device", "err", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: token, Path: "/", MaxAge: 400 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
	if isNew && hadOthers {
		s.Store.Audit(r.Context(), u, auditNewDevice, deviceDescription(r))
	}
}

// deviceDescription says where a login came from: the address, and the
// browser and system as the browser names them.
func deviceDescription(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if fromLoopback(r) {
		if fwd := strings.Split(r.Header.Get("X-Forwarded-For"), ","); fwd[0] != "" {
			ip = strings.TrimSpace(fwd[len(fwd)-1])
		}
	}
	ua := r.UserAgent()
	browser := "an unknown browser"
	for _, b := range []string{"Firefox", "Edg", "Chrome", "Safari"} {
		if strings.Contains(ua, b+"/") {
			browser = map[string]string{"Edg": "Edge"}[b]
			if browser == "" {
				browser = b
			}
			break
		}
	}
	system := ""
	for _, o := range []string{"Android", "iPhone", "iPad", "Windows", "Mac OS X", "Linux"} {
		if strings.Contains(ua, o) {
			system = " on " + map[string]string{"Mac OS X": "a Mac"}[o]
			if system == " on " {
				system = " on " + o
			}
			break
		}
	}
	return "from " + ip + ", " + browser + system
}
