package cloud

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"roostack.cloud/roostack/cloudapi"
)

// StorageKeys are short-lived keys for Roostack Cloud backup: a server's
// backups or the lab's prune (Cloud's DESIGN §6(b)).
func (s *Service) StorageKeys(ctx context.Context, req cloudapi.StorageRequest) (cloudapi.StorageKeys, error) {
	var k cloudapi.StorageKeys
	if !s.Status().Linked {
		return k, errors.New("the lab isn't linked to Roostack Cloud")
	}
	err := s.client.do(ctx, true, http.MethodPost, cloudapi.PathLabStorageKeys, req, &k)
	return k, err
}

// StorageUsage is what the lab stores in Roostack Cloud backup, against
// its quota (zero: it may not use it). The answer is kept, for
// Status.Storage. The quota is the account's, which its labs share, and
// the use this lab's (Cloud's DESIGN §6(b)).
func (s *Service) StorageUsage(ctx context.Context) (cloudapi.StorageUsage, error) {
	var u cloudapi.StorageUsage
	if !s.Status().Linked {
		return u, nil
	}
	if err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabStorage, nil, &u); err != nil {
		return u, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Linked {
		kept := u
		kept.Servers = slices.Clone(u.Servers)
		s.st.Storage = &kept
		if err := s.save(); err != nil {
			s.log.Warn("Roostack Cloud: keeping Cloud backup's space", "err", err)
		}
	}
	return u, nil
}
