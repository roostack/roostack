package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"roostack.cloud/roostack/internal/store"
)

// Roots are the certificate authorities checks trust (nil: the system's).
// Tests set their own.
var Roots *x509.CertPool

// Probe runs one check and says how it went, in words for people.
func Probe(ctx context.Context, c Check) store.MonitorResult {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := time.Now()
	var r store.MonitorResult
	switch {
	case c.URL != "":
		r = probeHTTP(ctx, c)
	case c.TCP != "":
		r = probeTCP(ctx, c.TCP)
	case c.Ping != "":
		r = probePing(ctx, c.Ping)
	default:
		r.Err = "there's nothing to check"
	}
	if r.Millis == 0 {
		r.Millis = int(time.Since(start).Milliseconds())
	}
	r.At = start
	return r
}

// probeHTTP asks for c.URL: up when it answers, within the timeout, with
// a status that counts and (unless SkipVerify) a valid certificate for
// its name. The certificate's dates are kept either way.
func probeHTTP(ctx context.Context, c Check) store.MonitorResult {
	var r store.MonitorResult
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" {
		r.Err = "that isn't a web address"
		return r
	}
	host := u.Hostname()
	var certErr error
	tlsConf := &tls.Config{
		ServerName: host,
		// Verified below instead, so an expired or wrong certificate still
		// gives its dates and the answer.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				certErr = errors.New("it sent no certificate")
				return nil
			}
			leaf := cs.PeerCertificates[0]
			r.CertIssued, r.CertExpires = leaf.NotBefore, leaf.NotAfter
			inter := x509.NewCertPool()
			for _, ic := range cs.PeerCertificates[1:] {
				inter.AddCert(ic)
			}
			_, certErr = leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: Roots})
			return nil
		},
	}
	dialer := &net.Dialer{}
	tr := &http.Transport{
		TLSClientConfig:   tlsConf,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if c.Dial != "" {
				addr = c.Dial
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ResponseHeaderTimeout: Timeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		r.Err = "that isn't a web address"
		return r
	}
	req.Header.Set("User-Agent", "Roostack monitoring")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		r.Err = netError(err, host)
		return r
	}
	r.Millis = max(int(time.Since(start).Milliseconds()), 1)
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	r.Status = resp.StatusCode
	ok := resp.StatusCode < 500
	if len(c.Statuses) > 0 {
		ok = slices.Contains(c.Statuses, resp.StatusCode)
	}
	switch {
	case u.Scheme == "https" && !c.SkipVerify && certErr != nil:
		r.Err = "its certificate isn't valid: " + certText(certErr)
	case !ok:
		r.Err = fmt.Sprintf("it answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	default:
		r.Up = true
	}
	return r
}

// certText says why a certificate isn't valid.
func certText(err error) string {
	var inv x509.CertificateInvalidError
	var host x509.HostnameError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &inv) && inv.Reason == x509.Expired:
		return "it has expired"
	case errors.As(err, &host):
		return "it's for another name"
	case errors.As(err, &unknown):
		return "no trusted authority signed it"
	}
	return err.Error()
}

// probeTCP connects to addr.
func probeTCP(ctx context.Context, addr string) store.MonitorResult {
	var r store.MonitorResult
	host, _, _ := net.SplitHostPort(addr)
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		r.Err = netError(err, host)
		return r
	}
	conn.Close()
	r.Millis = max(int(time.Since(start).Milliseconds()), 1)
	r.Up = true
	return r
}

// probePing sends one ping to host and waits for its answer, through an
// unprivileged ICMP socket (Linux's ping sockets, which
// net.ipv4.ping_group_range allows; docs/ASSUMPTIONS.md).
func probePing(ctx context.Context, host string) store.MonitorResult {
	var r store.MonitorResult
	ip, err := lookup(ctx, host)
	if err != nil {
		r.Err = netError(err, host)
		return r
	}
	start := time.Now()
	if err := ping(ctx, ip); err != nil {
		r.Err = netError(err, host)
		return r
	}
	r.Millis = max(int(time.Since(start).Milliseconds()), 1)
	r.Up = true
	return r
}

// lookup resolves host to one address, IPv4 first.
func lookup(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP, nil
		}
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs[0].IP, nil
}

// ping sends an echo request to ip and waits for the reply.
func ping(ctx context.Context, ip net.IP) error {
	family, proto, echo := syscall.AF_INET, syscall.IPPROTO_ICMP, byte(8)
	if ip.To4() == nil {
		family, proto, echo = syscall.AF_INET6, syscall.IPPROTO_ICMPV6, 128
	}
	// Close-on-exec is set under ForkLock, as the net package does where
	// SOCK_CLOEXEC is missing (macOS), so no child started meanwhile
	// inherits the socket.
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(family, syscall.SOCK_DGRAM, proto)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return fmt.Errorf("pinging isn't allowed here: %w", err)
	}
	f := os.NewFile(uintptr(fd), "ping")
	conn, err := net.FilePacketConn(f)
	f.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		conn.SetDeadline(d)
	}
	// The kernel sets the identifier (the socket's port) and, for ICMPv6,
	// the checksum; the IPv4 checksum is filled in here.
	msg := []byte{echo, 0, 0, 0, 0, 0, 0, 1, 'r', 'o', 'o', 's', 't', 'a', 'c', 'k'}
	if echo == 8 {
		sum := checksum(msg)
		msg[2], msg[3] = byte(sum>>8), byte(sum)
	}
	if _, err := conn.WriteTo(msg, &net.UDPAddr{IP: ip}); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		// An echo reply (0, or 129 for ICMPv6) to our sequence number.
		if n >= 8 && (buf[0] == 0 || buf[0] == 129) && buf[6] == 0 && buf[7] == 1 {
			return nil
		}
	}
}

// checksum is the Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s > 0xffff {
		s = s>>16 + s&0xffff
	}
	return ^uint16(s)
}

// netError says in plain words why a connection failed.
func netError(err error, host string) string {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return host + " isn't a name the network knows"
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return fmt.Sprintf("no answer within %d seconds", int(Timeout/time.Second))
	case errors.Is(err, syscall.ECONNREFUSED):
		return "nothing is listening there"
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH):
		return "the network can't reach it"
	case strings.Contains(err.Error(), "tls:"):
		return "the secure connection failed"
	}
	return err.Error()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
