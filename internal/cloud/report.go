package cloud

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"roostack.cloud/roostack/cloudapi"
)

// report tells Cloud what the lab reports after each check-in (Report):
// its own email recipients, who then get Cloud's emails about the lab
// too, and each server's last backup to Cloud (SPEC.md §6.6). It returns
// the addresses Cloud was told (nil: it wasn't). A Cloud from before
// reports answers 404: the lab carries on without, quietly.
func (s *Service) report(ctx context.Context) ([]string, error) {
	if s.Report == nil {
		return nil, nil
	}
	r, err := s.Report(ctx)
	if err != nil {
		return nil, err
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
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Reported = s.now()
	if r.Emails == nil {
		r.Emails = []string{}
	}
	return r.Emails, s.save()
}

// sameRecipients says Cloud's list of the lab's recipients has exactly
// the addresses reported, in any order: when it doesn't, the report
// changed them, and the lab asks how they stand now rather than a day
// later.
func sameRecipients(got []cloudapi.RecipientStatus, reported []string) bool {
	if len(got) != len(reported) {
		return false
	}
	for _, e := range reported {
		if !slices.ContainsFunc(got, func(r cloudapi.RecipientStatus) bool { return strings.EqualFold(r.Email, e) }) {
			return false
		}
	}
	return true
}

// withName is names with host's status replaced by st (nil: it works).
func withName(names []cloudapi.NameStatus, host string, st *cloudapi.NameStatus) []cloudapi.NameStatus {
	names = slices.DeleteFunc(slices.Clone(names), func(n cloudapi.NameStatus) bool { return n.Host == host })
	if st != nil {
		names = append(names, *st)
	}
	return names
}
