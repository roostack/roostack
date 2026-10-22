package cloud

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/cloud/cloudtest"
	"roostack.cloud/roostack/internal/testwait"
)

// A suspended account's lab keeps its link and checks in, and uses its
// backups in Cloud as far as the suspension allows (Cloud's backups
// choice): all of them, restores only, or none. What it may not do is
// refused with Cloud's message (423), the link kept (review X7).
func TestSuspendedBackups(t *testing.T) {
	for _, c := range []struct {
		backups         string
		backup, restore bool
	}{
		{cloudtest.BackupsAll, true, true},
		{cloudtest.BackupsRestore, false, true},
		{cloudtest.BackupsNone, false, false},
	} {
		t.Run("backups="+cmp.Or(c.backups, "none"), func(t *testing.T) {
			ctx := context.Background()
			f, s, _ := fakeCloud(t)
			key := "a recovery key"
			s.RecoveryKey = func() (string, error) { return key, nil }
			s.Report = func(context.Context) (cloudapi.LabReport, error) { return cloudapi.LabReport{Emails: []string{}}, nil }
			link(t, f, s, "")
			labID := s.Status().LabID
			testwait.For(t, 10*time.Second, func() bool { return f.Identity(labID) != nil })
			f.StorageQuota, f.StorageUsed = 100e9, 2e9
			f.SuspendedFor, f.SuspendedBackups = "Phishing", c.backups

			// The check-in, the report and the space used carry on.
			if err := s.Check(ctx); err != nil {
				t.Fatalf("check: %v", err)
			}
			st := s.Status()
			if !st.Linked || st.Suspended != "Phishing" || st.Refused != "" || st.LastError != "" ||
				st.Storage == nil || st.Storage.UsedBytes != 2e9 {
				t.Fatalf("status = %+v", st)
			}
			if _, ok := f.Reports[labID]; !ok {
				t.Error("the lab didn't report")
			}

			// A server's backups and the lab's prune, as the choice says.
			for _, req := range []cloudapi.StorageRequest{
				{Purpose: cloudapi.StorageBackup, Server: "n-0123456789"},
				{Purpose: cloudapi.StoragePrune},
			} {
				keys, err := s.StorageKeys(ctx, req)
				switch msg, refused := refusal(err); {
				case c.backup && (err != nil || keys.AccessKeyID == ""):
					t.Errorf("%s keys: %v", req.Purpose, err)
				case !c.backup && (!refused || !strings.Contains(msg, "suspended: Phishing")):
					t.Errorf("%s keys while backups are stopped: %v", req.Purpose, err)
				}
			}

			// Restoring with the recovery key, from a new install.
			fresh, err := New(t.TempDir(), s.cfg, "test", s.labName, s.log)
			if err != nil {
				t.Fatal(err)
			}
			acc, err := fresh.RestoreAccess(ctx, key)
			switch _, refused := refusal(err); {
			case c.restore && (err != nil || len(acc.Labs) != 1):
				t.Errorf("restore access: %+v, %v", acc, err)
			case !c.restore && !refused:
				t.Errorf("restore access while restores are stopped: %v", err)
			}

			// Nothing else of the lab's: its relay, say.
			if err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabRelayStats, nil, nil); !isStatus(err, http.StatusLocked) {
				t.Errorf("relay statistics while suspended: %v", err)
			}
			if !s.Status().Linked {
				t.Error("a refusal dropped the link")
			}
		})
	}
}

// A suspended account's lab whose certificate Cloud no longer accepts
// can't relink with its recovery key, but keeps the link and says why.
func TestSuspendedRelink(t *testing.T) {
	ctx := context.Background()
	f, s, _ := fakeCloud(t)
	s.RecoveryKey = func() (string, error) { return "a recovery key", nil }
	link(t, f, s, "")
	labID := s.Status().LabID
	testwait.For(t, 10*time.Second, func() bool { return f.Identity(labID) != nil })
	f.SuspendedFor = "Phishing"
	f.Revoke(labID)
	if err := s.Check(ctx); err == nil {
		t.Fatal("relinked while suspended")
	}
	if st := s.Status(); !st.Linked || !strings.Contains(st.Refused, "suspended: Phishing") || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	f.SuspendedFor = ""
	if err := s.Check(ctx); err != nil || s.Status().Refused != "" {
		t.Fatalf("after the suspension: %v, %+v", err, s.Status())
	}
}

// A lab past Cloud's limit on its changes (429) keeps its link: what it
// asked fails, its reads carry on, and a check-in whose report is held
// back still succeeds.
func TestWriteLimit(t *testing.T) {
	ctx := context.Background()
	f, s, _ := fakeCloud(t)
	s.Report = func(context.Context) (cloudapi.LabReport, error) { return cloudapi.LabReport{Emails: []string{}}, nil }
	link(t, f, s, "")
	f.StorageQuota = 100e9
	f.Writes, f.WriteLimit = nil, 1
	req := cloudapi.StorageRequest{Purpose: cloudapi.StorageBackup, Server: "n-0123456789"}
	if _, err := s.StorageKeys(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StorageKeys(ctx, req); !isStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("keys past the limit: %v", err)
	}
	if _, err := s.StorageUsage(ctx); err != nil {
		t.Errorf("reading past the limit: %v", err)
	}
	if err := s.Check(ctx); err != nil || f.Reports != nil {
		t.Errorf("check past the limit: %v, reported %+v", err, f.Reports)
	}
	if st := s.Status(); !st.Linked || st.LastError != "" || st.Refused != "" {
		t.Errorf("status = %+v", st)
	}
	f.Writes = nil
	if _, err := s.StorageKeys(ctx, req); err != nil {
		t.Errorf("keys once the limit allows: %v", err)
	}
}

func isStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}
