package web

import (
	"context"
	"fmt"
	"time"

	"roostack.cloud/roostack/authkit"
)

// codeResult is the outcome of checking a two-factor code.
type codeResult int

const (
	codeOK codeResult = iota
	codeWrong
	// codeLocked: too many wrong codes for the account, so none is
	// accepted for now (authkit.TOTPLockedUntil).
	codeLocked
)

// checkCode checks a user's two-factor code, at login or before showing
// the recovery key, and counts wrong ones against the account, however
// many logins they're spread over (SPEC.md §6.10). A code is accepted once
// (UseTOTPStep), and not at all while the account is locked; until says
// when that ends. Checks run one at a time, so guesses sent all at once
// can't slip past the count.
func (s *Server) checkCode(ctx context.Context, userID int64, code string) (res codeResult, until time.Time, err error) {
	s.codeMu.Lock()
	defer s.codeMu.Unlock()
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return 0, time.Time{}, err
	}
	now := time.Now()
	if until := authkit.TOTPLockedUntil(u.TOTPFailures, u.TOTPFailedAt); now.Before(until) {
		return codeLocked, until, nil
	}
	if step, ok := authkit.VerifyTOTP(u.TOTPSecret, code, now, u.TOTPLastStep); ok {
		if ok, err = s.Store.UseTOTPStep(ctx, u.ID, step); err != nil || ok {
			return codeOK, time.Time{}, err
		}
	}
	n, err := s.Store.AddTOTPFailure(ctx, u.ID, now)
	if err != nil {
		return 0, time.Time{}, err
	}
	if until := authkit.TOTPLockedUntil(n, now); !until.IsZero() {
		s.Log.Warn("two-factor codes locked", "user", u.Username, "wrong codes", n, "until", until)
		s.Store.Audit(ctx, u, "2fa locked", fmt.Sprintf("%d wrong codes in a row; none accepted until %s",
			n, until.Local().Format("2 January 15:04")))
		return codeLocked, until, nil
	}
	return codeWrong, time.Time{}, nil
}

// lockedMessage tells someone when codes work again, and what it means if
// they didn't enter the wrong ones.
func lockedMessage(until time.Time) string {
	until = until.Local()
	when := until.Format("15:04")
	if until.Format(time.DateOnly) != time.Now().Format(time.DateOnly) {
		when = until.Format("15:04 on 2 January")
	}
	return "Too many wrong two-factor codes for this account, so no code will work until " + when +
		". If you didn't enter them, someone knows your password: change it once you're back in."
}
