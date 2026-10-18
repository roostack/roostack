package cloud

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"roostack.cloud/roostack/cloudapi"
)

// report tells Cloud what the lab reports after each check-in (Report):
// its own email recipients, who then get Cloud's emails about the lab
// too, and each server's last backup to Cloud (SPEC.md §6.6). A Cloud from
// before reports answers 404: the lab carries on without, quietly.
func (s *Service) report(ctx context.Context) error {
	if s.Report == nil {
		return nil
	}
	r, err := s.Report(ctx)
	if err != nil {
		return err
	}
	if len(r.Emails) > cloudapi.MaxReportEmails {
		r.Emails = r.Emails[:cloudapi.MaxReportEmails]
	}
	if len(r.Backups) > cloudapi.MaxReportServers {
		r.Backups = r.Backups[:cloudapi.MaxReportServers]
	}
	err = s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabReport, r, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Reported = s.now()
	return s.save()
}

// withName is names with host's status replaced by st (nil: it works).
func withName(names []cloudapi.NameStatus, host string, st *cloudapi.NameStatus) []cloudapi.NameStatus {
	names = slices.DeleteFunc(slices.Clone(names), func(n cloudapi.NameStatus) bool { return n.Host == host })
	if st != nil {
		names = append(names, *st)
	}
	return names
}
