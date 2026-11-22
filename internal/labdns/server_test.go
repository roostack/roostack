package labdns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

var (
	homeDevice = netip.MustParseAddr("192.168.1.50")
	vpnDevice  = netip.MustParseAddr("100.96.128.7")
	outsider   = netip.MustParseAddr("203.0.113.9")
)

func testServer(t *testing.T) (*Server, *[]string) {
	t.Helper()
	s := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		Domain:    "Lab.Example.com",
		Names:     map[string]string{"photos.lab.example.com": "192.168.1.10", "my.lab.example.com.": "192.168.1.10"},
		CheckAddr: "192.168.1.10",
	})
	var asked []string
	s.Exchange = func(_ context.Context, server string, q []byte, _ bool) ([]byte, error) {
		asked = append(asked, server)
		var p dnsmessage.Parser
		h, err := p.Start(q)
		if err != nil {
			return nil, err
		}
		qq, err := p.Question()
		if err != nil {
			return nil, err
		}
		a := netip.MustParseAddr("198.51.100.1")
		return reply(h, &qq, dnsmessage.RCodeSuccess, &a, false), nil
	}
	s.upstreams = []string{"192.168.1.1"}
	return s, &asked
}

func question(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 4242, RecursionDesired: true})
	b.StartQuestions()
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	q, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return q
}

type answer struct {
	rcode dnsmessage.RCode
	addrs []string
	auth  bool
}

func parse(t *testing.T, b []byte) answer {
	t.Helper()
	var m dnsmessage.Message
	if err := m.Unpack(b); err != nil {
		t.Fatalf("unpacking the answer: %v", err)
	}
	if m.ID != 4242 || !m.Response {
		t.Fatalf("header = %+v", m.Header)
	}
	a := answer{rcode: m.RCode, auth: m.Authoritative}
	for _, r := range m.Answers {
		if ar, ok := r.Body.(*dnsmessage.AResource); ok {
			a.addrs = append(a.addrs, netip.AddrFrom4(ar.A).String())
		}
	}
	return a
}

func TestAnswer(t *testing.T) {
	cases := []struct {
		name   string
		q      string
		typ    dnsmessage.Type
		from   netip.Addr
		want   answer
		passed bool
	}{
		{"a lab name", "photos.lab.example.com.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"a lab name in another case", "PHOTOS.Lab.Example.COM.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"a VPN device", "my.lab.example.com.", dnsmessage.TypeA, vpnDevice, answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"IPv6 for a lab name is empty", "photos.lab.example.com.", dnsmessage.TypeAAAA, homeDevice, answer{auth: true}, false},
		{"HTTPS for a lab name is empty", "photos.lab.example.com.", dnsmessage.TypeHTTPS, homeDevice, answer{auth: true}, false},
		{"other types are passed on", "photos.lab.example.com.", dnsmessage.TypeTXT, homeDevice, answer{addrs: []string{"198.51.100.1"}}, true},
		{"another name under the domain is passed on", "www.lab.example.com.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"198.51.100.1"}}, true},
		{"anything else is passed on", "example.org.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"198.51.100.1"}}, true},
		{"Firefox's signal", "use-application-dns.net.", dnsmessage.TypeA, homeDevice, answer{rcode: dnsmessage.RCodeNameError}, false},
		{"a check's name", "check-abc123.lab.example.com.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"a check's name two levels down isn't one", "check-x.y.lab.example.com.", dnsmessage.TypeA, homeDevice, answer{addrs: []string{"198.51.100.1"}}, true},
		{"loopback", "photos.lab.example.com.", dnsmessage.TypeA, netip.MustParseAddr("127.0.0.1"), answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"an IPv4 address in IPv6", "photos.lab.example.com.", dnsmessage.TypeA, netip.MustParseAddr("::ffff:192.168.1.50"), answer{addrs: []string{"192.168.1.10"}, auth: true}, false},
		{"someone outside", "example.org.", dnsmessage.TypeA, outsider, answer{rcode: dnsmessage.RCodeRefused}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, asked := testServer(t)
			got := parse(t, s.Answer(t.Context(), question(t, c.q, c.typ), c.from, false))
			if got.rcode != c.want.rcode || !slices.Equal(got.addrs, c.want.addrs) || got.auth != c.want.auth {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
			if passed := len(*asked) > 0; passed != c.passed {
				t.Errorf("passed on = %v, want %v", passed, c.passed)
			}
		})
	}
}

func TestNothingAnswersUpstream(t *testing.T) {
	s, _ := testServer(t)
	s.Exchange = func(context.Context, string, []byte, bool) ([]byte, error) { return nil, errors.New("timed out") }
	s.upstreams = []string{"192.168.1.1", "9.9.9.9"}
	if got := parse(t, s.Answer(t.Context(), question(t, "example.org.", dnsmessage.TypeA), homeDevice, false)); got.rcode != dnsmessage.RCodeServerFailure {
		t.Errorf("rcode = %v, want SERVFAIL", got.rcode)
	}
}

func TestNotQuestions(t *testing.T) {
	s, asked := testServer(t)
	if out := s.Answer(t.Context(), []byte{1, 2, 3}, homeDevice, false); out != nil {
		t.Errorf("a short packet got %x", out)
	}
	// An answer (QR set) isn't answered.
	q := question(t, "example.org.", dnsmessage.TypeA)
	q[2] |= 0x80
	if out := s.Answer(t.Context(), q, homeDevice, false); out != nil {
		t.Errorf("an answer got %x", out)
	}
	if len(*asked) > 0 {
		t.Errorf("passed on %v", *asked)
	}
}

func TestSeenAndDevices(t *testing.T) {
	s, _ := testServer(t)
	name := CheckName("abc123", "lab.example.com")
	if s.Seen(name) {
		t.Fatal("seen before it was asked")
	}
	s.Answer(t.Context(), question(t, name+".", dnsmessage.TypeA), homeDevice, false)
	s.Answer(t.Context(), question(t, "example.org.", dnsmessage.TypeA), netip.MustParseAddr("192.168.1.51"), false)
	s.Answer(t.Context(), question(t, "example.org.", dnsmessage.TypeA), vpnDevice, false)
	if !s.Seen(name) || !s.Seen("CHECK-abc123.lab.example.com.") {
		t.Error("not seen after it was asked")
	}
	if s.Seen(CheckName("other", "lab.example.com")) {
		t.Error("another check's name seen")
	}
	// Home addresses only: a VPN device is counted by its person's
	// devices, not here.
	if got := s.Status().Devices; got != 2 {
		t.Errorf("devices = %d, want 2", got)
	}
	path := filepath.Join(t.TempDir(), "clients.json")
	if err := s.saveClients(path); err != nil {
		t.Fatal(err)
	}
	s2, _ := testServer(t)
	s2.loadClients(path)
	if got := s2.Status().Devices; got != 2 {
		t.Errorf("devices after loading = %d, want 2", got)
	}
}

func TestRefresh(t *testing.T) {
	dir := t.TempDir()
	resolved := filepath.Join(dir, "resolved.conf")
	plain := filepath.Join(dir, "resolv.conf")
	os.WriteFile(plain, []byte("nameserver 127.0.0.53\noptions edns0\n"), 0o644)
	os.WriteFile(resolved, []byte("# from the network\nnameserver 192.168.1.10\nnameserver 192.168.1.1\nnameserver 192.168.1.1\nnameserver fd00::1\n"), 0o644)
	s, _ := testServer(t)
	s.ResolvConfs = []string{resolved, plain}
	s.Own = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.10")} }
	s.Exchange = func(context.Context, string, []byte, bool) ([]byte, error) { return nil, errors.New("refused") }
	s.Refresh(t.Context())
	if st := s.Status(); !slices.Equal(st.Upstreams, []string{"192.168.1.1", "fd00::1"}) || st.Fallback {
		t.Errorf("status = %+v: its own address and the duplicate should go", st)
	}

	// Only the stub (loopback) and itself: the fallback.
	os.Remove(resolved)
	s.Refresh(t.Context())
	if st := s.Status(); !slices.Equal(st.Upstreams, []string{Fallback}) || !st.Fallback || st.Why == "" {
		t.Errorf("status = %+v, want the fallback", st)
	}
	s.SetConfig(Config{Fallback: "1.1.1.1"})
	s.Refresh(t.Context())
	if st := s.Status(); !slices.Equal(st.Upstreams, []string{"1.1.1.1"}) {
		t.Errorf("status = %+v, want the Owner's fallback", st)
	}
}

func TestRefreshLoop(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "resolv.conf")
	os.WriteFile(conf, []byte("nameserver 192.168.1.1\nnameserver 192.168.1.2\n"), 0o644)
	s, _ := testServer(t)
	s.ResolvConfs = []string{conf}
	s.Own = func() []netip.Addr { return nil }
	// The router (192.168.1.1) sends questions back to the lab's DNS
	// server; the other doesn't.
	s.Exchange = func(ctx context.Context, server string, q []byte, tcp bool) ([]byte, error) {
		if server == "192.168.1.1" {
			return s.Answer(ctx, q, netip.MustParseAddr("192.168.1.1"), tcp), nil
		}
		return nil, errors.New("NXDOMAIN, say")
	}
	s.Refresh(t.Context())
	if st := s.Status(); !slices.Equal(st.Upstreams, []string{"192.168.1.2"}) {
		t.Errorf("status = %+v, want only the one that doesn't send questions back", st)
	}
	os.WriteFile(conf, []byte("nameserver 192.168.1.1\n"), 0o644)
	s.Refresh(t.Context())
	if st := s.Status(); !st.Fallback || st.Why != "the router sends questions back to the lab's DNS server" {
		t.Errorf("status = %+v, want the fallback because of the loop", st)
	}
}

// TestRun answers over UDP and TCP on a real port, stops when the names
// file goes, and the socket answers the daemon.
func TestRun(t *testing.T) {
	if testing.Short() {
		t.Skip("listens on ports")
	}
	dir := t.TempDir()
	names := filepath.Join(dir, "names.json")
	if err := Write(names, Config{Domain: "lab.example.com", Names: map[string]string{"photos.lab.example.com": "192.168.1.10"}}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()
	sock := filepath.Join(dir, "dns.sock")
	done := make(chan error, 1)
	go func() {
		done <- Run(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{NamesFile: names, Socket: sock, StateDir: dir, Port: port})
	}()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var got answer
	for i := 0; ; i++ {
		b, err := exchangeAt(t, "udp", addr, question(t, "photos.lab.example.com.", dnsmessage.TypeA))
		if err == nil {
			got = parse(t, b)
			break
		}
		if i > 50 {
			t.Fatalf("no answer over UDP: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !slices.Equal(got.addrs, []string{"192.168.1.10"}) {
		t.Errorf("UDP: %+v", got)
	}
	b, err := exchangeAt(t, "tcp", addr, question(t, "photos.lab.example.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, b); !slices.Equal(got.addrs, []string{"192.168.1.10"}) {
		t.Errorf("TCP: %+v", got)
	}
	c := Client{Socket: sock}
	if st, err := c.Status(t.Context()); err != nil || st.Since.IsZero() {
		t.Errorf("status = %+v, %v", st, err)
	}
	if seen, err := c.Seen(t.Context(), "check-x.lab.example.com"); err != nil || seen {
		t.Errorf("seen = %v, %v", seen, err)
	}
	if err := Remove(names); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still running after the names file went")
	}
}

func exchangeAt(t *testing.T, network, addr string, q []byte) ([]byte, error) {
	t.Helper()
	conn, err := net.DialTimeout(network, addr, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if network == "tcp" {
		if err := writeTCP(conn, q); err != nil {
			return nil, err
		}
		return readTCP(conn)
	}
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	return buf[:n], err
}

// FuzzAnswer: whatever arrives, it never panics, and anything it sends
// back can be read as a DNS answer.
func FuzzAnswer(f *testing.F) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("photos.lab.example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	seed, _ := b.Finish()
	f.Add(seed)
	f.Add([]byte{})
	f.Add([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 1, 0, 1}) // a pointer to itself
	f.Fuzz(func(t *testing.T, q []byte) {
		s := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Domain: "lab.example.com",
			Names: map[string]string{"photos.lab.example.com": "192.168.1.10"}, CheckAddr: "192.168.1.10"})
		s.Exchange = func(context.Context, string, []byte, bool) ([]byte, error) { return nil, errors.New("offline") }
		s.upstreams = []string{"192.168.1.1"}
		out := s.Answer(t.Context(), q, homeDevice, false)
		if out == nil {
			return
		}
		var m dnsmessage.Message
		if err := m.Unpack(out); err != nil {
			t.Fatalf("answer %x to %x can't be read: %v", out, q, err)
		}
	})
}
