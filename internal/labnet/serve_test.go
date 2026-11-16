package labnet

import (
	"bytes"
	stdlog "log"
	"log/slog"
	"strings"
	"testing"
)

// The controller's check that a server answers (a connection closed
// before its TLS handshake) isn't logged; other server errors are.
func TestServerErrors(t *testing.T) {
	var buf bytes.Buffer
	l := stdlog.New(serverErrors{slog.New(slog.NewTextHandler(&buf, nil))}, "", 0)
	l.Printf("http: TLS handshake error from 100.96.0.1:51234: EOF")
	if buf.Len() != 0 {
		t.Errorf("logged the check: %s", buf.String())
	}
	l.Printf("http: TLS handshake error from 100.96.0.9:4000: remote error: tls: bad certificate")
	if !strings.Contains(buf.String(), "bad certificate") {
		t.Errorf("not logged: %q", buf.String())
	}
}
