package attention

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/store"
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
	GroupProblems = "problems"
	GroupWaiting  = "waiting"
	GroupSecurity = "security"
	GroupWeekly   = "weekly"
)

// NotifyGroup is a group someone can choose, for the account page. Need is
// the permission it takes: Members can't act on the lab's problems, and
// lab security is the Owners'.
type NotifyGroup struct {
	ID, Title, Hint string
	Need            auth.Permission
}

// NotifyGroups are the groups, in the order the account page lists them.
var NotifyGroups = []NotifyGroup{
	{GroupProblems, "Problems", "Backups failing, a disk running low, a service or server that stopped working, or updates that failed. Roostack will email you once a problem has lasted a few minutes, then once a day while it lasts, and again when it's fixed.", auth.ManageServices},
	{GroupWaiting, "Waiting for you", "Something Roostack can't finish without you, such as a restart to finish installing updates, once it has waited a day.", auth.ManageServices},
	{GroupSecurity, "Security for the lab", "A new Owner or Admin, someone's password or two-factor reset, a server joining the lab, or another lab starting to store backups here.", auth.ManageHost},
	{GroupWeekly, "Weekly summary", "On Monday mornings: backups, disk space, attacks blocked on your public addresses, and anything still needing attention.", auth.ManageServices},
}

// ProblemRecipients are the addresses the lab's problems are emailed to
// (SPEC.md §6.13): each person with an address who gets the Problems
// group. The lab reports them to Roostack Cloud, whose emails about the
// lab go to them too (SPEC.md §6.6).
func ProblemRecipients(ctx context.Context, st Store) ([]string, error) {
	users, err := st.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for i := range users {
		u := &users[i]
		if e := strings.ToLower(u.Email); e != "" && UserGroups(u)[GroupProblems] && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// GroupsFor are the groups a role can choose.
func GroupsFor(role string) []NotifyGroup {
	var out []NotifyGroup
	for _, g := range NotifyGroups {
		if auth.Can(role, g.Need) {
			out = append(out, g)
		}
	}
	return out
}

// Audit actions that are security events (beside the existing ones).
const (
	AuditLocked       = "2fa locked"
	AuditNewDevice    = "new device"
	AuditServerJoined = "server joined"
	AuditGuestJoined  = "storage guest joined"
	// Pausing public access and opening it again (SPEC.md §6.16), with
	// what in the detail ("everything", "services built with php",
	// "Lychee"): every Owner hears at once, so a pause nobody expected is
	// seen.
	AuditPublicPaused = "public access paused"
	AuditPublicOpened = "public access opened"
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

// UserGroups are the groups someone gets: what they chose, or their
// role's defaults (everything but the weekly summary).
func UserGroups(u *store.User) map[string]bool {
	out := map[string]bool{}
	var chosen []string
	switch u.Notify {
	case "":
		chosen = []string{GroupProblems, GroupWaiting, GroupSecurity}
	case "none":
	default:
		chosen = strings.Split(u.Notify, ",")
	}
	for _, g := range GroupsFor(u.Role) {
		if slices.Contains(chosen, g.ID) {
			out[g.ID] = true
		}
	}
	return out
}

// Notifier emails each person what they chose to hear about (SPEC.md
// §6.13): security events from the audit log, problems and what waits for
// them from Needs attention as they'd see it, and the weekly summary.
type Notifier struct {
	Store Store
	Log   *slog.Logger
	// Mail sends the emails (nil: none can be sent).
	Mail Mailer
	// Lab is the lab as it is at each run.
	Lab func() *Lab
}

// Store is what the notifier reads and keeps (store.Store): the people,
// the audit log, what each was told, and its own settings.
type Store interface {
	ListUsers(ctx context.Context) ([]store.User, error)
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	LastAuditID(ctx context.Context) (int64, error)
	AuditSince(ctx context.Context, id int64) ([]store.AuditEntry, error)
	Notices(ctx context.Context, userID int64) (map[string]store.Notice, error)
	SaveNotice(ctx context.Context, userID int64, n store.Notice) error
	DeleteNotice(ctx context.Context, userID int64, key string) error
}

// Mailer sends email through the lab's SMTP server (mail.Service).
type Mailer interface {
	Settings() (config.Mail, bool)
	Send(ctx context.Context, msg mail.Message) error
}

// Run sends notifications every minute until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	sleep.Every(ctx, time.Minute, func() {
		if err := n.Notify(ctx, time.Now()); err != nil {
			n.Log.Error("notifications", "err", err)
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

// Notify does one run: security events since the last run, then each
// person's problems.
func (n *Notifier) Notify(ctx context.Context, now time.Time) error {
	users, err := n.Store.ListUsers(ctx)
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
	if n.Mail != nil {
		m, _ := n.Mail.Settings()
		configured = m.Configured()
	}
	lab := n.Lab()

	// Security: audit rows since the last run (the first run starts from
	// now: the history isn't emailed).
	cursor, _ := n.Store.Setting(ctx, auditCursorKey)
	last, _ := strconv.ParseInt(cursor, 10, 64)
	if cursor == "" {
		last, _ = n.Store.LastAuditID(ctx)
	} else {
		entries, err := n.Store.AuditSince(ctx, last)
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
					if auth.Can(u.Role, auth.ManageHost) && UserGroups(u)[GroupSecurity] && !strings.EqualFold(u.Username, subject) {
						mailFor(u).security = append(mailFor(u).security, labWide)
					}
				}
			}
		}
	}
	n.Store.SetSetting(ctx, auditCursorKey, strconv.FormatInt(last, 10))
	if !configured {
		return nil // nothing can be sent; problems are followed once it can
	}

	for i := range users {
		u := &users[i]
		groups := UserGroups(u)
		if u.Email == "" {
			continue
		}
		m := mailFor(u)
		if groups[GroupProblems] || groups[GroupWaiting] {
			if err := n.followProblems(ctx, lab, u, groups, now, m); err != nil {
				n.Log.Error("notifications: following problems", "user", u.Username, "err", err)
			}
		}
		if groups[GroupWeekly] {
			m.weekly = n.weeklySummary(ctx, lab, u, now)
		}
		if m.empty() {
			continue
		}
		subject, body := composeNotification(lab, u, m)
		if err := n.Mail.Send(ctx, mail.Message{To: []string{u.Email}, Subject: subject, Body: body}); err != nil {
			n.Log.Error("notifications: sending", "user", u.Username, "err", err)
			continue
		}
		n.markSent(ctx, u, now, m)
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
	case AuditNewDevice:
		return "Someone logged in to your account from a new browser or device (" + when + ", " + e.Detail + "). If it wasn't you, change your password.", "", e.Username
	case AuditLocked:
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
	case AuditPublicPaused:
		return "", "Public access to " + strings.TrimSuffix(e.Detail, " via command line") + " was paused " + by + " (" + when + "). Visitors from the internet are seeing a page " +
			"saying it's closed for a while, and it's still working at home.", ""
	case AuditPublicOpened:
		return "", "Public access to " + strings.TrimSuffix(e.Detail, " via command line") + " was opened again " + by + " (" + when + ").", ""
	}
	return "", "", ""
}

// followProblems compares what needs attention for u now with what was
// seen before: new ones once they've lasted long enough, reminders, and
// the ones that cleared.
func (n *Notifier) followProblems(ctx context.Context, lab *Lab, u *store.User, groups map[string]bool, now time.Time, m *notifyMail) error {
	known, err := n.Store.Notices(ctx, u.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, it := range lab.For(ctx, u) {
		if it.Level != Problem && it.Level != Warning || !groups[it.Group] {
			continue
		}
		waiting := it.Group == GroupWaiting
		key := it.Key
		seen[key] = true
		nt, ok := known[key]
		if !ok {
			nt = store.Notice{Key: key, FirstSeen: now}
		}
		nt.GoneSince = time.Time{} // back (or never gone): not fixed
		nt.Text = it.Text + lab.linkLine(it.Link)
		delay := notifyDelay
		if waiting {
			delay = notifyWaitingDelay
		}
		switch {
		case nt.SentAt.IsZero() && now.Sub(nt.FirstSeen) >= delay:
			m.fresh, m.keys = append(m.fresh, nt.Text), append(m.keys, key)
		case !nt.SentAt.IsZero() && now.Sub(nt.SentAt) >= notifyRemind:
			m.still = append(m.still, nt.Text+"\n  (since "+nt.FirstSeen.Local().Format("2 January at 15:04")+")")
			m.keys = append(m.keys, key)
		}
		if err := n.Store.SaveNotice(ctx, u.ID, nt); err != nil {
			return err
		}
	}
	// Gone: "Fixed" once it has stayed gone as long as a problem must last
	// to be emailed, so a moment's gap (a server between retries) isn't
	// news. Never emailed: forgotten at once.
	for key, nt := range known {
		if seen[key] {
			continue
		}
		if !nt.SentAt.IsZero() {
			if nt.GoneSince.IsZero() {
				nt.GoneSince = now
				if err := n.Store.SaveNotice(ctx, u.ID, nt); err != nil {
					return err
				}
				continue
			}
			if now.Sub(nt.GoneSince) < notifyDelay {
				continue
			}
			m.fixed = append(m.fixed, words.FirstLine(nt.Text))
		}
		if err := n.Store.DeleteNotice(ctx, u.ID, key); err != nil {
			return err
		}
	}
	return nil
}

// markSent records what was emailed, after it went.
func (n *Notifier) markSent(ctx context.Context, u *store.User, now time.Time, m *notifyMail) {
	if len(m.keys) > 0 {
		known, err := n.Store.Notices(ctx, u.ID)
		if err != nil {
			return
		}
		for _, key := range m.keys {
			if nt, ok := known[key]; ok {
				nt.SentAt = now
				n.Store.SaveNotice(ctx, u.ID, nt)
			}
		}
	}
	if m.weekly != "" {
		n.Store.SetSetting(ctx, weeklyKey+strconv.FormatInt(u.ID, 10), strconv.FormatInt(now.Unix(), 10))
	}
}

// uiURL is Roostack's own address, for links in emails: UIHost, or
// roost.<domain> when it isn't set.
func (lab *Lab) uiURL(path string) string {
	if lab.Domain == "" {
		return ""
	}
	host := lab.UIHost
	if host == "" {
		host = "roost." + lab.Domain
	}
	return "https://" + host + path
}

func (lab *Lab) linkLine(path string) string {
	if u := lab.uiURL(path); u != "" && path != "" {
		return "\n  " + u
	}
	return ""
}

// composeNotification writes one person's email.
func composeNotification(l *Lab, u *store.User, m *notifyMail) (subject, body string) {
	lab := l.LabName
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
	if link := l.uiURL("/account/notifications"); link != "" {
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
