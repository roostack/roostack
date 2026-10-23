package attention

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/store"
)

// fakeMailer keeps what it was asked to send.
type fakeMailer struct {
	m    config.Mail
	sent []mail.Message
}

func (f *fakeMailer) Settings() (config.Mail, bool) { return f.m, false }
func (f *fakeMailer) Send(_ context.Context, msg mail.Message) error {
	f.sent = append(f.sent, msg)
	return nil
}

// newNotifier is a notifier for newLab's lab, with sam (an Owner, at
// sam@example.com) and kim (a Member).
func newNotifier(t *testing.T) (*Notifier, *Lab, *fakeNode, *fakeMailer, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/roostack.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	id, _ := st.CreateUser(ctx, store.User{Username: "sam", Role: auth.RoleOwner, TOTPSecret: "x"})
	st.SetEmail(ctx, id, "sam@example.com")
	id, _ = st.CreateUser(ctx, store.User{Username: "kim", Role: auth.RoleMember})
	st.SetEmail(ctx, id, "kim@example.com")
	lab, _, fn := newLab()
	lab.LabName = "Smith family"
	fm := &fakeMailer{m: config.Mail{Host: "smtp.example.com", From: "roostack@example.com"}}
	n := &Notifier{Store: st, Log: slog.New(slog.DiscardHandler), Mail: fm, Lab: func() *Lab { return lab }}
	return n, lab, fn, fm, st
}

// A problem is emailed once it has lasted a few minutes, reminded daily,
// and "Fixed" once it has stayed gone as long; Members aren't told.
func TestNotifierFollowsProblems(t *testing.T) {
	n, _, fn, fm, _ := newNotifier(t)
	ctx := context.Background()
	t0 := time.Now()
	n.Notify(ctx, t0)
	fn.backups.st = backup.Status{Configured: true, Progress: -1, Last: &store.BackupRun{Error: "boom"}, LastOK: fn.backups.st.LastOK}
	if n.Notify(ctx, t0.Add(time.Minute)); len(fm.sent) != 0 {
		t.Fatalf("too soon: %+v", fm.sent)
	}
	n.Notify(ctx, t0.Add(12*time.Minute))
	if len(fm.sent) != 1 {
		t.Fatalf("sent %d", len(fm.sent))
	}
	m := fm.sent[0]
	if !slices.Equal(m.To, []string{"sam@example.com"}) || m.Subject != "[Smith family] Needs attention: The last backup didn't finish: boom" ||
		m.Body != "Hello sam,\n\nNeeds attention:\n\n- The last backup didn't finish: boom\n  https://roost.lab.example.com/backups\n\n"+
			"-- \nRoostack, for Smith family.\nChoose which emails you get: https://roost.lab.example.com/account/notifications\n" {
		t.Errorf("email: %+v", m)
	}
	if n.Notify(ctx, t0.Add(30*time.Minute)); len(fm.sent) != 1 {
		t.Error("said once")
	}
	if n.Notify(ctx, t0.Add(25*time.Hour)); len(fm.sent) != 2 || !strings.HasPrefix(fm.sent[1].Subject, "[Smith family] Still needs attention: ") ||
		!strings.Contains(fm.sent[1].Body, "  (since ") {
		t.Errorf("a day later: %+v", fm.sent[len(fm.sent)-1])
	}
	fn.backups.st.Last = fn.backups.st.LastOK
	if n.Notify(ctx, t0.Add(26*time.Hour)); len(fm.sent) != 2 {
		t.Error("fixed only once it has stayed fixed")
	}
	if n.Notify(ctx, t0.Add(26*time.Hour+11*time.Minute)); len(fm.sent) != 3 || fm.sent[2].Subject != "[Smith family] Fixed: The last backup didn't finish: boom" {
		t.Errorf("fixed: %+v", fm.sent[len(fm.sent)-1])
	}
}

// Nothing is sent, and no problem followed, until email is set up; the
// audit log is followed from the first run, not from its start.
func TestNotifierWithoutMail(t *testing.T) {
	n, _, fn, fm, st := newNotifier(t)
	ctx := context.Background()
	u, _ := st.UserByUsername(ctx, "sam")
	st.Audit(ctx, u, "password changed", "")
	fm.m = config.Mail{}
	fn.backups.st = backup.Status{Progress: -1}
	t0 := time.Now()
	n.Notify(ctx, t0)
	n.Notify(ctx, t0.Add(time.Hour))
	if notices, _ := st.Notices(ctx, u.ID); len(notices) != 0 || len(fm.sent) != 0 {
		t.Errorf("followed %v, sent %+v", notices, fm.sent)
	}
	fm.m = config.Mail{Host: "smtp.example.com", From: "roostack@example.com"}
	n.Notify(ctx, t0.Add(2*time.Hour))
	n.Notify(ctx, t0.Add(3*time.Hour))
	if len(fm.sent) != 1 || strings.Contains(fm.sent[0].Body, "password") {
		t.Errorf("history emailed, or no problem: %+v", fm.sent)
	}
}

// Security events: about your own account always, and the lab's to its
// Owners who chose them.
func TestSecurityEvents(t *testing.T) {
	n, _, _, fm, st := newNotifier(t)
	ctx := context.Background()
	n.Notify(ctx, time.Now())
	kim, _ := st.UserByUsername(ctx, "kim")
	st.Audit(ctx, kim, AuditNewDevice, "from 192.0.2.7, Firefox on Linux")
	st.Audit(ctx, nil, "user add", "alex (admin)")
	n.Notify(ctx, time.Now())
	byTo := map[string]mail.Message{}
	for _, m := range fm.sent {
		byTo[m.To[0]] = m
	}
	if m := byTo["kim@example.com"]; !strings.HasPrefix(m.Subject, "[Smith family] Security: Someone logged in to your account from a new browser or device (") ||
		strings.Contains(m.Body, "alex") {
		t.Errorf("kim: %+v", m)
	}
	if m := byTo["sam@example.com"]; !strings.Contains(m.Body, "- A new account: alex (admin), added from the server's command line (") {
		t.Errorf("sam: %+v", m)
	}

	at := time.Date(2026, 10, 5, 9, 30, 0, 0, time.Local)
	for _, c := range []struct {
		e                      store.AuditEntry
		own, labWide, whoseAcc string
	}{
		{store.AuditEntry{Action: "password reset", Username: "sam", Detail: "kim", At: at},
			"Your password was reset by sam (5 October at 09:30). If you didn't ask for this, tell your lab's Owner.",
			"kim's password was reset by sam (5 October at 09:30).", "kim"},
		{store.AuditEntry{Action: AuditLocked, Username: "kim", Detail: "at login", At: at},
			"Several wrong two-factor codes were entered for your account, so codes are locked for a while (5 October at 09:30, at login).",
			"kim's two-factor codes were locked after several wrong codes (5 October at 09:30).", "kim"},
		{store.AuditEntry{Action: "user role", Username: "sam", Detail: "kim → member", At: at}, "", "", ""},
		{store.AuditEntry{Action: AuditGuestJoined, Username: "system", Detail: "Jones lab", At: at},
			"", "Another lab started storing its backups here: Jones lab (5 October at 09:30).", ""},
	} {
		own, labWide, whose := securityEvent(c.e)
		if own != c.own || labWide != c.labWide || whose != c.whoseAcc {
			t.Errorf("%s: %q, %q, %q", c.e.Action, own, labWide, whose)
		}
	}
}

// Who gets which groups: their choice, or their role's defaults, and
// never a group their role can't have.
func TestUserGroups(t *testing.T) {
	keys := func(m map[string]bool) []string {
		var out []string
		for _, g := range NotifyGroups {
			if m[g.ID] {
				out = append(out, g.ID)
			}
		}
		return out
	}
	for _, c := range []struct {
		role, notify string
		want         []string
	}{
		{auth.RoleOwner, "", []string{GroupProblems, GroupWaiting, GroupSecurity}},
		{auth.RoleAdmin, "", []string{GroupProblems, GroupWaiting}},
		{auth.RoleMember, "", nil},
		{auth.RoleOwner, "weekly,security", []string{GroupSecurity, GroupWeekly}},
		{auth.RoleMember, "problems,weekly", nil},
		{auth.RoleOwner, "none", nil},
	} {
		if got := keys(UserGroups(&store.User{Role: c.role, Notify: c.notify})); !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %v", c.role, c.notify, got)
		}
	}
}

// A long first line is cut in the subject, which counts the rest.
func TestSubject(t *testing.T) {
	long := strings.Repeat("a", 90)
	subject, body := composeNotification(&Lab{}, &store.User{Username: "sam"}, &notifyMail{fresh: []string{long, "b"}})
	if subject != "[Your lab] Needs attention: "+strings.Repeat("a", 77)+"… (and 1 more)" {
		t.Errorf("subject %q", subject)
	}
	if strings.Contains(body, "Choose which emails") {
		t.Error("no address, no link")
	}
}

// The Monday summary comes once a week from 8 am.
func TestWeeklyOnMondays(t *testing.T) {
	n, lab, _, _, st := newNotifier(t)
	ctx := context.Background()
	u, _ := st.UserByUsername(ctx, "sam")
	monday := time.Date(2026, 10, 5, 7, 59, 0, 0, time.Local)
	if s := n.weeklySummary(ctx, lab, u, monday); s != "" {
		t.Errorf("before 8: %q", s)
	}
	s := n.weeklySummary(ctx, lab, u, monday.Add(time.Minute))
	if !strings.HasPrefix(s, "This server:\n- Last backup: ") || !strings.HasSuffix(s, "\nNothing needs attention.\n") {
		t.Errorf("summary:\n%s", s)
	}
	n.markSent(ctx, u, monday, &notifyMail{weekly: s})
	if s := n.weeklySummary(ctx, lab, u, monday.Add(time.Hour)); s != "" {
		t.Error("twice")
	}
	if s := n.weeklySummary(ctx, lab, u, monday.Add(7*24*time.Hour+time.Minute)); s == "" {
		t.Error("next week")
	}
}
