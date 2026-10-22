// Package usererr is the one kind of error a person can fix: a whole
// sentence saying what to change, which a page shows as it is. Any other
// error is the server's problem: the page says something went wrong, and
// the detail goes to the log.
package usererr

import (
	"errors"
	"fmt"
)

// Error is an error meant for people, such as "Enter a port number, such
// as 2222."
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// New makes an Error, formatting its message as fmt.Sprintf does.
func New(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// Message is the sentence of the first Error in err's chain, and whether
// there is one.
func Message(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg, true
	}
	return "", false
}
