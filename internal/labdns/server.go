package labdns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/trust"
)

// TTL is how long devices may keep the lab's own answers: short, so that
// a name moving to another server (a node serving it directly) shows
// quickly.
const TTL = 60

// How long it remembers things.
const (
	seenFor    = 15 * time.Minute   // a check's name (Seen allows checkFor)
	checkFor   = 10 * time.Minute   // how long after a check's look-up its answer counts
	clientsFor = 7 * 24 * time.Hour // a home address, for Status.Devices
)

// firefoxCanary is the name Firefox asks before turning on its own DNS
// over HTTPS: NXDOMAIN keeps it on the network's DNS, as Pi-hole does.
const firefoxCanary = "use-application-dns.net"

// loopSuffix ends the names it asks its own upstreams to find whether
// they send questions back to it (a router whose own DNS is the lab's).
const loopSuffix = ".loop-check.roostack.invalid"

// Server answers DNS. Its zero value isn't ready: use NewServer.
type Server struct {
	Log *slog.Logger
	// Exchange passes a question on to server (an address, no port) and
	// returns its answer; tests replace it.
	Exchange func(ctx context.Context, server string, q []byte, tcp bool) ([]byte, error)
	// Own lists this server's own addresses, which are never upstreams
	// (it would ask itself); tests replace it.
	Own func() []netip.Addr
	// ResolvConfs are the files it reads upstreams from, the first that
	// has any: systemd-resolved's real upstreams, else the usual file.
	ResolvConfs []string

	cfg atomic.Pointer[Config]

	mu        sync.Mutex
	upstreams []string
	fallback  bool
	why       string
	seen      map[string]time.Time
	clients   map[netip.Addr]time.Time
	probes    map[string]bool // loop check names asked, true once they came back
	since     time.Time
}

// NewServer returns a server answering with c.
func NewServer(log *slog.Logger, c Config) *Server {
	s := &Server{Log: log, Exchange: exchange, Own: ownAddrs,
		ResolvConfs: []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"},
		seen:        map[string]time.Time{}, clients: map[netip.Addr]time.Time{}, probes: map[string]bool{},
		since: time.Now()}
	s.SetConfig(c)
	return s
}

// SetConfig changes what it answers.
func (s *Server) SetConfig(c Config) {
	names := make(map[string]string, len(c.Names))
	for n, a := range c.Names {
		names[normal(n)] = a
	}
	c.Names, c.Domain = names, normal(c.Domain)
	s.cfg.Store(&c)
}

// normal is a name as it compares: lower case, no trailing dot.
func normal(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// allowed: who may ask. The firewall already lets only the home network
// and the lab's VPN devices reach it; this refuses everyone else too, so
// it can never be used to send answers at someone else.
func allowed(a netip.Addr) bool {
	return a.IsLoopback() || trust.DevicesRange.Contains(a) || slices.ContainsFunc(trust.HomeRanges, func(p netip.Prefix) bool { return p.Contains(a) })
}

func home(a netip.Addr) bool {
	return slices.ContainsFunc(trust.HomeRanges, func(p netip.Prefix) bool { return p.Contains(a) })
}

// Answer is the reply to the question q from from, nil to send nothing
// (it couldn't be read as a question at all).
func (s *Server) Answer(ctx context.Context, q []byte, from netip.Addr, tcp bool) []byte {
	from = from.Unmap()
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil || h.Response {
		return nil
	}
	question, qerr := p.Question()
	switch {
	case !allowed(from):
		return reply(h, nil, dnsmessage.RCodeRefused, nil, false)
	case qerr != nil:
		return reply(h, nil, dnsmessage.RCodeFormatError, nil, false)
	case h.OpCode != 0:
		return reply(h, &question, dnsmessage.RCodeNotImplemented, nil, false)
	}
	now := time.Now()
	name := normal(question.Name.String())
	s.mu.Lock()
	if home(from) {
		s.clients[from] = now
	}
	_, probe := s.probes[name]
	if probe {
		s.probes[name] = true
	}
	s.mu.Unlock()
	if probe {
		// One of its own loop checks came back: refused, so it goes no
		// further round.
		return reply(h, &question, dnsmessage.RCodeRefused, nil, false)
	}
	if name == firefoxCanary {
		return reply(h, &question, dnsmessage.RCodeNameError, nil, false)
	}
	c := s.cfg.Load()
	addr, ours := c.Names[name]
	if label, ok := strings.CutSuffix(name, "."+c.Domain); ok && c.Domain != "" && strings.HasPrefix(label, CheckPrefix) && !strings.Contains(label, ".") {
		s.mu.Lock()
		s.seen[name] = now
		for n, t := range s.seen {
			if now.Sub(t) > seenFor {
				delete(s.seen, n)
			}
		}
		s.mu.Unlock()
		addr, ours = c.CheckAddr, c.CheckAddr != ""
	}
	if ours && question.Class == dnsmessage.ClassINET {
		switch question.Type {
		case dnsmessage.TypeA:
			if a, err := netip.ParseAddr(addr); err == nil && a.Is4() {
				return reply(h, &question, dnsmessage.RCodeSuccess, &a, true)
			}
		case dnsmessage.TypeAAAA, dnsmessage.TypeHTTPS:
			// Roostack sets no such records, and one from upstream could
			// send a device somewhere else.
			return reply(h, &question, dnsmessage.RCodeSuccess, nil, true)
		}
	}
	if b := s.passOn(ctx, q, tcp); b != nil {
		return b
	}
	return reply(h, &question, dnsmessage.RCodeServerFailure, nil, false)
}

// reply builds an answer to the question q (nil: none could be read),
// with a as its A record if set.
func reply(h dnsmessage.Header, q *dnsmessage.Question, rcode dnsmessage.RCode, a *netip.Addr, authoritative bool) []byte {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: h.ID, Response: true, OpCode: h.OpCode,
		Authoritative: authoritative, RecursionDesired: h.RecursionDesired, RecursionAvailable: true, RCode: rcode})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil
	}
	if q != nil {
		if err := b.Question(*q); err != nil {
			return nil
		}
		if a != nil {
			if err := b.StartAnswers(); err != nil {
				return nil
			}
			rh := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: TTL}
			if err := b.AResource(rh, dnsmessage.AResource{A: a.As4()}); err != nil {
				return nil
			}
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// passOn asks the upstreams in turn, returning the first answer as it
// came, or nil if none answered.
func (s *Server) passOn(ctx context.Context, q []byte, tcp bool) []byte {
	s.mu.Lock()
	ups := slices.Clone(s.upstreams)
	s.mu.Unlock()
	for _, u := range ups {
		wait := 2 * time.Second
		if tcp {
			wait = 5 * time.Second
		}
		c, cancel := context.WithTimeout(ctx, wait)
		b, err := s.Exchange(c, u, q, tcp)
		cancel()
		if err == nil {
			return b
		}
		s.Log.Debug("passing a question on", "to", u, "err", err)
	}
	return nil
}

// exchange is Exchange over the network, on port 53.
func exchange(ctx context.Context, server string, q []byte, tcp bool) ([]byte, error) {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, net.JoinHostPort(server, "53"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if tcp {
		if err := writeTCP(conn, q); err != nil {
			return nil, err
		}
		return readTCP(conn)
	}
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		// Only the answer to this question (same ID).
		if n >= 2 && len(q) >= 2 && buf[0] == q[0] && buf[1] == q[1] {
			return slices.Clone(buf[:n]), nil
		}
	}
}

// DNS over TCP: each message after its length in two bytes.
func writeTCP(w io.Writer, m []byte) error {
	b := make([]byte, 2+len(m))
	binary.BigEndian.PutUint16(b, uint16(len(m)))
	copy(b[2:], m)
	_, err := w.Write(b)
	return err
}

func readTCP(r io.Reader) ([]byte, error) {
	var n [2]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	m := make([]byte, binary.BigEndian.Uint16(n[:]))
	_, err := io.ReadFull(r, m)
	return m, err
}

// ownAddrs are this server's addresses.
func ownAddrs() []netip.Addr {
	addrs, _ := net.InterfaceAddrs()
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr())
		}
	}
	return out
}

// readUpstreams are the nameservers in the first of the files that
// names any, as written.
func readUpstreams(files []string) []string {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var out []string
		for line := range strings.Lines(string(b)) {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "nameserver" {
				out = append(out, fields[1])
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// Refresh works out the upstreams again: the server's own DNS servers,
// leaving out its own addresses and loopback (systemd-resolved's stub,
// which might ask the lab's DNS server back), and any that sends a
// question back to it. With none left, the fallback.
func (s *Server) Refresh(ctx context.Context) {
	own := s.Own()
	var ups []string
	for _, u := range readUpstreams(s.ResolvConfs) {
		a, err := netip.ParseAddr(u)
		if err != nil || a.IsLoopback() || a.IsUnspecified() || slices.Contains(own, a.WithZone("")) || slices.Contains(own, a) {
			continue
		}
		if !slices.Contains(ups, u) {
			ups = append(ups, u)
		}
	}
	why := ""
	if len(ups) == 0 {
		why = "this server's network gives no other DNS server"
	} else {
		var kept []string
		for _, u := range ups {
			if s.loops(ctx, u) {
				s.Log.Warn("a DNS server sends questions back to the lab's DNS server, so it won't be asked", "server", u)
				continue
			}
			kept = append(kept, u)
		}
		if len(kept) == 0 {
			why = "the router sends questions back to the lab's DNS server"
		}
		ups = kept
	}
	fallback := why != ""
	if fallback {
		ups = []string{cmpOr(s.cfg.Load().Fallback, Fallback)}
	}
	s.mu.Lock()
	changed := !slices.Equal(ups, s.upstreams)
	s.upstreams, s.fallback, s.why = ups, fallback, why
	s.mu.Unlock()
	if changed {
		s.Log.Info("passing other questions on", "to", strings.Join(ups, ", "), "fallback", fallback, "why", why)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// loops asks upstream for a name nobody else would, and says whether the
// question came back to this server within two seconds.
func (s *Server) loops(ctx context.Context, upstream string) bool {
	var r [6]byte
	rand.Read(r[:])
	name := hex.EncodeToString(r[:]) + loopSuffix
	s.mu.Lock()
	s.probes[name] = false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.probes, name)
		s.mu.Unlock()
	}()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: binary.BigEndian.Uint16(r[:2]), RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name + "."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	q, err := b.Finish()
	if err != nil {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.Exchange(c, upstream, q, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes[name]
}

// Seen says whether it was asked for name in the last ten minutes: a
// check's page loads the name at once, and someone without scripts
// chooses Check this device a little later.
func (s *Server) Seen(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.seen[normal(name)]
	return ok && time.Since(t) <= checkFor
}

// Status says how it's doing.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.clients {
		if time.Since(t) <= clientsFor {
			n++
		}
	}
	return Status{Upstreams: slices.Clone(s.upstreams), Fallback: s.fallback, Why: s.why, Devices: n, Since: s.since}
}

// The home addresses that asked, kept across restarts (StateDir).
func (s *Server) loadClients(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]int64
	if json.Unmarshal(b, &m) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for a, t := range m {
		if addr, err := netip.ParseAddr(a); err == nil && time.Since(time.Unix(t, 0)) <= clientsFor {
			s.clients[addr] = time.Unix(t, 0)
		}
	}
}

func (s *Server) saveClients(path string) error {
	s.mu.Lock()
	m := map[string]int64{}
	for a, t := range s.clients {
		if time.Since(t) > clientsFor {
			delete(s.clients, a)
			continue
		}
		m[a.String()] = t.Unix()
	}
	s.mu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, b, 0o600)
}

// Options are where Run finds and keeps things (the constants when
// empty).
type Options struct {
	NamesFile, Socket, StateDir string
	Port                        int
}

// Run answers DNS until ctx ends or the names file is gone.
func Run(ctx context.Context, log *slog.Logger, o Options) error {
	o.NamesFile, o.Socket, o.StateDir = cmpOr(o.NamesFile, NamesFile), cmpOr(o.Socket, Socket), cmpOr(o.StateDir, StateDir)
	if o.Port == 0 {
		o.Port = Port
	}
	c, err := Read(o.NamesFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := NewServer(log, c)
	clients := filepath.Join(o.StateDir, "clients.json")
	s.loadClients(clients)
	defer s.saveClients(clients)

	addr := fmt.Sprintf(":%d", o.Port)
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	os.Remove(o.Socket)
	sock, err := net.Listen("unix", o.Socket)
	if err != nil {
		return err
	}
	defer sock.Close()
	go s.serveSocket(sock)
	go s.serveUDP(ctx, pc)
	go s.serveTCP(ctx, ln)
	// Upstreams need the listeners: a loop check comes back to them.
	s.Refresh(ctx)
	log.Info("answering DNS", "port", o.Port, "names", len(c.Names))

	confs := s.ResolvConfs
	lastConf := readUpstreams(confs)
	info, _ := os.Stat(o.NamesFile)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	refreshed, saved := time.Now(), time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		now, err := os.Stat(o.NamesFile)
		if errors.Is(err, os.ErrNotExist) {
			log.Info("the names file is gone, so the lab's DNS server is stopping")
			return nil
		}
		if err == nil && (info == nil || !now.ModTime().Equal(info.ModTime()) || now.Size() != info.Size()) {
			if c, err := Read(o.NamesFile); err == nil {
				s.SetConfig(c)
				log.Info("names changed", "names", len(c.Names))
			} else {
				log.Error("reading the names file", "err", err)
			}
			info = now
		}
		// Upstreams again when the network's change, and every 15 minutes
		// (a router changed to send questions back).
		if conf := readUpstreams(confs); !slices.Equal(conf, lastConf) || time.Since(refreshed) > 15*time.Minute {
			lastConf, refreshed = conf, time.Now()
			s.Refresh(ctx)
		}
		if time.Since(saved) > 10*time.Minute {
			saved = time.Now()
			if err := s.saveClients(clients); err != nil {
				log.Warn("saving the addresses that asked", "err", err)
			}
		}
	}
}

func (s *Server) serveUDP(ctx context.Context, pc net.PacketConn) {
	sem := make(chan struct{}, 256)
	for {
		buf := make([]byte, 4096)
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				s.Log.Error("reading DNS over UDP", "err", err)
			}
			return
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			continue // too busy: the device will ask again
		}
		go func() {
			defer func() { <-sem }()
			if out := s.Answer(ctx, buf[:n], ua.AddrPort().Addr(), false); out != nil {
				pc.WriteTo(out, from)
			}
		}()
	}
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) {
	sem := make(chan struct{}, 64)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				s.Log.Error("accepting DNS over TCP", "err", err)
			}
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			defer conn.Close()
			ta, ok := conn.RemoteAddr().(*net.TCPAddr)
			if !ok {
				return
			}
			for {
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				q, err := readTCP(conn)
				if err != nil {
					return
				}
				out := s.Answer(ctx, q, ta.AddrPort().Addr(), true)
				if out == nil || writeTCP(conn, out) != nil {
					return
				}
			}
		}()
	}
}

// serveSocket answers the daemon (Client).
func (s *Server) serveSocket(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(s.Status())
	})
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"seen": s.Seen(r.URL.Query().Get("name"))})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	srv.Serve(ln)
}
