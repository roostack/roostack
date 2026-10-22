// Package mail sends email through the lab's SMTP server (SPEC.md §6.13):
// plain text with the same in HTML beside it (mailkit), over STARTTLS
// (port 587) or TLS (465), with the standard library's SMTP client.
// Settings are read for every message, so a change in Settings → Email
// applies at once.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/mailkit"
)

// ErrNotConfigured means no SMTP server is set up.
var ErrNotConfigured = errors.New("email isn't set up")

// Message is one email.
type Message struct {
	To      []string
	Subject string
	Body    string // plain text
	// HTML is the email in HTML, sent beside Body. Nil makes it from Body
	// (mailkit.FromText), headed by the subject and signed with the lab's
	// name.
	HTML *mailkit.Email
}

// Service sends email with the lab's settings, and saves them.
type Service struct {
	// LabPath is lab.yaml; PasswordFile holds the SMTP password.
	LabPath, PasswordFile string
	// Timeout limits connecting and sending (default 30 seconds).
	Timeout time.Duration
	// TLS checks the server's certificate (nil: against the system's
	// certificate authorities; tests trust their own).
	TLS *tls.Config
}

// Settings are the saved settings, and whether a password is saved.
func (s *Service) Settings() (config.Mail, bool) {
	lab, _ := config.Load(s.LabPath)
	_, err := os.Stat(s.PasswordFile)
	return lab.Mail, err == nil
}

// Save stores the settings; password replaces the saved one unless it's
// empty (keepPassword) or clearPassword is set.
func (s *Service) Save(m config.Mail, password string, clearPassword bool) error {
	if err := check(m); err != nil {
		return err
	}
	if err := config.Update(s.LabPath, func(l *config.Lab) { l.Mail = m }); err != nil {
		return err
	}
	switch {
	case clearPassword:
		if err := os.Remove(s.PasswordFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case password != "":
		if err := fsutil.WritePrivate(s.PasswordFile, []byte(password)); err != nil {
			return err
		}
	}
	return nil
}

// FormError is a setting someone can fix; its message says how.
type FormError struct{ Msg string }

func (e *FormError) Error() string { return e.Msg }

func check(m config.Mail) error {
	switch {
	case m.Host == "" || strings.ContainsAny(m.Host, " /:@"):
		return &FormError{"Enter the SMTP server's name, such as smtp.fastmail.com."}
	case m.Port < 1 || m.Port > 65535:
		return &FormError{"Enter the SMTP server's port: usually 587, or 465."}
	case m.Security != config.MailSTARTTLS && m.Security != config.MailTLS:
		return &FormError{"Choose how the connection is encrypted."}
	}
	if _, err := mail.ParseAddress(m.From); err != nil || strings.ContainsAny(m.From, "\r\n") {
		return &FormError{"Enter the address emails come from, such as roostack@example.com."}
	}
	return nil
}

// Send sends msg with the saved settings.
func (s *Service) Send(ctx context.Context, msg Message) error {
	m, _ := s.Settings()
	if !m.Configured() {
		return ErrNotConfigured
	}
	password, err := os.ReadFile(s.PasswordFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.send(ctx, m, strings.TrimSpace(string(password)), msg)
}

func (s *Service) send(ctx context.Context, m config.Mail, password string, msg Message) error {
	var to []string
	for _, a := range msg.To {
		addr, err := mail.ParseAddress(a)
		if err != nil || strings.ContainsAny(a, "\r\n") {
			return &FormError{fmt.Sprintf("%q isn't an email address.", a)}
		}
		to = append(to, addr.Address)
	}
	if len(to) == 0 {
		return &FormError{"Give an address to send to."}
	}
	from, _ := mail.ParseAddress(m.From)
	if msg.HTML == nil {
		msg.HTML = &mailkit.Email{Brand: mailkit.Roostack, Heading: msg.Subject, Blocks: mailkit.FromText(msg.Body),
			Footer: []mailkit.Block{mailkit.Paragraph("Roostack, for " + config.NameAt(s.LabPath, config.Lab{})() + ".")}}
	}
	body, err := compose(from, to, msg)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.TLS != nil {
		tlsCfg = s.TLS.Clone()
	}
	tlsCfg.ServerName = m.Host
	d := &net.Dialer{}
	var conn net.Conn
	if m.Security == config.MailTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("couldn't reach %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%s didn't answer as an SMTP server: %w", addr, err)
	}
	defer c.Close()
	if m.Security == config.MailSTARTTLS {
		// Never send the password unencrypted: STARTTLS is required.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s doesn't offer STARTTLS: choose TLS (port 465) instead, if it supports that", addr)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("encrypting the connection to %s: %w", addr, err)
		}
	}
	if m.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.Username, password, m.Host)); err != nil {
			return fmt.Errorf("%s refused the username or password: %w", m.Host, err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("%s refused the sender %s: %w", m.Host, from.Address, err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("%s refused the recipient %s: %w", m.Host, r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%s didn't accept the message: %w", m.Host, err)
	}
	return c.Quit()
}

// compose builds the message: headers, then the body, its text and HTML
// as alternatives (mailkit.Body).
func compose(from *mail.Address, to []string, msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, errors.New("a subject can't contain line breaks")
	}
	id := make([]byte, 12)
	rand.Read(id)
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", msg.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), domain)
	contentType, body, err := mailkit.Body(msg.Body, *msg.HTML)
	if err != nil {
		return nil, err
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: " + contentType + "\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n\r\n")
	b.Write(body)
	return b.Bytes(), nil
}
