package cloud

import (
	"context"
	"errors"
	"net/http"

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
// its quota (zero: it may not use it).
func (s *Service) StorageUsage(ctx context.Context) (cloudapi.StorageUsage, error) {
	var u cloudapi.StorageUsage
	if !s.Status().Linked {
		return u, nil
	}
	err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabStorage, nil, &u)
	return u, err
}
