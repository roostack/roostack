package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// fakeSMTP is a small SMTP server: STARTTLS (or TLS from the start), AUTH
// PLAIN, one message. It records what it got.
type fakeSMTP struct {
	ln        net.Listener
	tls       *tls.Config
	implicit  bool // TLS from the start (port 465)
	noTLS     bool // offers no STARTTLS
	mu        sync.Mutex
	auth      string // "user:password" as received
	encrypted bool   // the password came over TLS
	from      string
	rcpt      []string
	data      string
}

func newFakeSMTP(t *testing.T, implicit bool) (*fakeSMTP, *tls.Config) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srvTLS := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, tls: srvTLS, implicit: implicit}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f, &tls.Config{RootCAs: pool}
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	encrypted := f.implicit
	if f.implicit {
		c = tls.Server(c, f.tls)
	}
	r, w := bufio.NewReader(c), c
	say := func(s string) { w.Write([]byte(s + "\r\n")) }
	say("220 localhost ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch up := strings.ToUpper(cmd); {
		case strings.HasPrefix(up, "EHLO"):
			if !encrypted && !f.noTLS {
				say("250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN")
			} else {
				say("250-localhost\r\n250 AUTH PLAIN")
			}
		case up == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, f.tls)
			if tc.Handshake() != nil {
				return
			}
			c, encrypted = tc, true
			r, w = bufio.NewReader(c), c
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			f.mu.Lock()
			f.auth, f.encrypted = parts[1]+":"+parts[2], encrypted
			f.mu.Unlock()
			if parts[2] != "secret" {
				say("535 bad credentials")
				continue
			}
			say("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = cmd[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, cmd[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case up == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("500 what")
		}
	}
}

func newService(t *testing.T, m config.Mail, trust *tls.Config) *Service {
	t.Helper()
	dir := t.TempDir()
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.Mail = m
	path := filepath.Join(dir, "lab.yaml")
	if err := lab.Save(path); err != nil {
		t.Fatal(err)
	}
	return &Service{LabPath: path, PasswordFile: filepath.Join(dir, "mail-password"), TLS: trust, Timeout: 5 * time.Second}
}

// A message goes out over STARTTLS with the password encrypted, as UTF-8
// quoted-printable text; the password is kept apart, 0600.
func TestSendSTARTTLS(t *testing.T) {
	f, trust := newFakeSMTP(t, false)
	s := newService(t, config.Mail{}, trust)
	m := config.Mail{Host: "localhost", Port: f.port(), Security: config.MailSTARTTLS, Username: "sam@example.com",
		From: "Roostack <roostack@lab.example.com>"}
	if err := s.Save(m, "secret", false); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(s.PasswordFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v %v", fi, err)
	}
	if got, saved := s.Settings(); got != m || !saved {
		t.Errorf("settings: %+v %v", got, saved)
	}
	err := s.Send(context.Background(), Message{To: []string{"sam@example.com"}, Subject: "Backups on nüc", Body: "Line one\nLine two — done."})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "sam@example.com:secret" || !f.encrypted || f.from != "<roostack@lab.example.com>" || len(f.rcpt) != 1 {
		t.Errorf("auth %q encrypted %v from %q rcpt %v", f.auth, f.encrypted, f.from, f.rcpt)
	}
	for _, want := range []string{"From: \"Roostack\" <roostack@lab.example.com>", "Subject: =?utf-8?q?Backups_on_n=C3=BCc?=",
		"Content-Type: text/plain; charset=utf-8", "Line one\r\nLine two =E2=80=94 done.", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(f.data, want) {
			t.Errorf("message missing %q:\n%s", want, f.data)
		}
	}
	// Saving again without a password keeps it.
	s.Save(m, "", false)
	if b, _ := os.ReadFile(s.PasswordFile); string(b) != "secret" {
		t.Error("the password should be kept")
	}
}

// TLS from the start (port 465) works; a wrong password and a server
// without STARTTLS are refused with a sentence, and the password never
// goes out unencrypted.
func TestSendProblems(t *testing.T) {
	f, trust := newFakeSMTP(t, true)
	s := newService(t, config.Mail{Host: "localhost", Port: f.port(), Security: config.MailTLS, Username: "sam", From: "r@lab.example.com"}, trust)
	os.WriteFile(s.PasswordFile, []byte("secret"), 0o600)
	if err := s.Send(context.Background(), Message{To: []string{"sam@example.com"}, Subject: "Hi", Body: "Hi"}); err != nil {
		t.Fatalf("TLS: %v", err)
	}
	os.WriteFile(s.PasswordFile, []byte("wrong"), 0o600)
	if err := s.Send(context.Background(), Message{To: []string{"sam@example.com"}, Subject: "Hi", Body: "Hi"}); err == nil ||
		!strings.Contains(err.Error(), "refused the username or password") {
		t.Errorf("wrong password: %v", err)
	}

	plain, trust2 := newFakeSMTP(t, false)
	plain.noTLS = true
	s2 := newService(t, config.Mail{Host: "localhost", Port: plain.port(), Security: config.MailSTARTTLS, Username: "sam", From: "r@lab.example.com"}, trust2)
	os.WriteFile(s2.PasswordFile, []byte("secret"), 0o600)
	if err := s2.Send(context.Background(), Message{To: []string{"sam@example.com"}, Subject: "Hi", Body: "Hi"}); err == nil ||
		!strings.Contains(err.Error(), "doesn't offer STARTTLS") {
		t.Errorf("no STARTTLS: %v", err)
	}
	if plain.auth != "" {
		t.Error("the password went out unencrypted")
	}

	if err := s2.Send(context.Background(), Message{To: []string{"not an address"}, Subject: "Hi"}); err == nil {
		t.Error("a bad recipient")
	}
	if err := newService(t, config.Mail{}, nil).Send(context.Background(), Message{To: []string{"a@b.c"}}); err != ErrNotConfigured {
		t.Errorf("not configured: %v", err)
	}
	if err := s.Save(config.Mail{Host: "smtp.example.com", Port: 587, Security: config.MailSTARTTLS, From: "nope"}, "", false); err == nil {
		t.Error("a bad sender address")
	}
}
