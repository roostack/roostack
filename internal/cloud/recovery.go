package cloud

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
)

// The recovery identity (SPEC.md §6.9, "Restoring from Roostack Cloud"):
// a signing key derived from the recovery key, whose public half the lab
// registers with Cloud. Whoever has the printed recovery key can then
// prove it to Cloud, to restore the lab from its Cloud backups or relink
// it, without the recovery key leaving the server. The recovery key is
// 256 random bits (authkit.NewToken), so it's derived directly (HKDF);
// a password would need a slow derivation instead.

// identityInfo names the derivation; a new version would change it.
const identityInfo = "roostack recovery identity v1"

// RecoveryIdentity derives the lab's recovery identity from its recovery
// key (as typed or kept: surrounding spaces don't count).
func RecoveryIdentity(recoveryKey string) (ed25519.PrivateKey, error) {
	key := strings.TrimSpace(recoveryKey)
	if key == "" {
		return nil, errors.New("no recovery key")
	}
	seed, err := hkdf.Key(sha256.New, []byte(key), nil, identityInfo, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// prove asks Cloud for a challenge and signs it for purpose.
func prove(ctx context.Context, c *client, id ed25519.PrivateKey, purpose string) (cloudapi.RestoreProof, error) {
	pub := id.Public().(ed25519.PublicKey)
	var ch cloudapi.RestoreChallenge
	if err := c.do(ctx, false, http.MethodPost, cloudapi.PathRestoreChallenge, cloudapi.RestoreChallengeRequest{PublicKey: pub}, &ch); err != nil {
		return cloudapi.RestoreProof{}, err
	}
	return cloudapi.RestoreProof{PublicKey: pub, Challenge: ch.Challenge,
		Signature: ed25519.Sign(id, cloudapi.RestoreMessage(purpose, ch.Challenge))}, nil
}

// RestoreAccess asks Cloud, with the recovery key, for keys to read the
// lab's backups there: for a new install restoring a lab that has lost
// its controller. It needs no link.
func RestoreAccess(ctx context.Context, cfg config.Cloud, recoveryKey string) (cloudapi.RestoreAccess, error) {
	c, err := newClient(cfg, func() *tls.Certificate { return nil })
	if err != nil {
		return cloudapi.RestoreAccess{}, err
	}
	id, err := RecoveryIdentity(recoveryKey)
	if err != nil {
		return cloudapi.RestoreAccess{}, err
	}
	proof, err := prove(ctx, c, id, cloudapi.ProveAccess)
	if err != nil {
		return cloudapi.RestoreAccess{}, err
	}
	var out cloudapi.RestoreAccess
	err = c.do(ctx, false, http.MethodPost, cloudapi.PathRestoreAccess, proof, &out)
	return out, err
}

// identity is the lab's recovery identity, or nil if there's no recovery
// key yet (no backups set up).
func (s *Service) identity() (ed25519.PrivateKey, error) {
	if s.RecoveryKey == nil {
		return nil, nil
	}
	key, err := s.RecoveryKey()
	if err != nil || strings.TrimSpace(key) == "" {
		return nil, err
	}
	return RecoveryIdentity(key)
}

// RegisterIdentity tells Cloud the lab's recovery identity, if it has
// changed since Cloud was last told (the first check-in after linking, or
// a new recovery key). Checking in calls it; changing the recovery key
// calls it at once, so the old key stops working with Cloud too.
func (s *Service) RegisterIdentity(ctx context.Context) error {
	id, err := s.identity()
	if err != nil || id == nil {
		return err
	}
	pub := id.Public().(ed25519.PublicKey)
	s.mu.Lock()
	linked, same := s.st.Linked, bytes.Equal(s.st.RecoveryIdentity, pub)
	s.mu.Unlock()
	if !linked || same {
		return nil
	}
	if err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRecoveryIdentity, cloudapi.RecoveryIdentity{PublicKey: pub}, nil); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.RecoveryIdentity = pub
	s.log.Info("Roostack Cloud: the lab's recovery identity was registered")
	return s.save()
}

// retryLater: Cloud couldn't be reached, or was busy (rate limited, or
// its own failure). Only a refusal means the lab can't relink.
func retryLater(err error) bool {
	var unreachable *Unreachable
	var cerr *Error
	return errors.As(err, &unreachable) ||
		errors.As(err, &cerr) && (cerr.Status == http.StatusTooManyRequests || cerr.Status >= 500)
}

// relink gets the lab a new certificate by proving its recovery key, when
// Cloud no longer accepts the one it has (it expired while the lab was
// off, or a restored lab's backup held an old one). Cloud refuses it for
// a lab that was unlinked, and it must be this lab: a restored lab with a
// recovery key that belongs to another would otherwise take its place.
func (s *Service) relink(ctx context.Context) error {
	id, err := s.identity()
	if err != nil {
		return err
	}
	if id == nil {
		return errors.New("no recovery key to prove the lab with")
	}
	s.mu.Lock()
	labID := s.st.LabID
	s.mu.Unlock()
	proof, err := prove(ctx, s.client, id, cloudapi.ProveRelink)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: labID}}, key)
	if err != nil {
		return err
	}
	var rl cloudapi.Relinked
	if err := s.client.do(ctx, false, http.MethodPost, cloudapi.PathRestoreRelink, cloudapi.RelinkRequest{RestoreProof: proof, CSR: csr}, &rl); err != nil {
		return err
	}
	if rl.LabID != labID {
		return fmt.Errorf("the recovery key belongs to another lab in Roostack Cloud (%s, not %s)", rl.LabID, labID)
	}
	cert, err := certFor(key, rl.Certificate)
	if err != nil {
		return err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveCert(kp, []byte(rl.Certificate)); err != nil {
		return err
	}
	s.cert = cert
	s.st.RecoveryIdentity = proof.PublicKey
	s.client.renewed()
	s.log.Info("Roostack Cloud: the lab was relinked with its recovery key", "until", cert.Leaf.NotAfter)
	return s.save()
}
