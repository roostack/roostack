package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/trust"
)

func TestWireGuardKeys(t *testing.T) {
	// A key pair from wg genkey | wg pubkey.
	pub, err := WireGuardPublicKey("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=")
	if err != nil || pub != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Errorf("public key = %q, %v", pub, err)
	}
	k, err := NewWireGuardKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WireGuardPublicKey(k); err != nil {
		t.Error(err)
	}
}

func TestWireGuardValidate(t *testing.T) {
	good := WireGuard{Address: "100.96.0.2/24", Peers: []Peer{{PublicKey: "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=",
		AllowedIPs: []string{"100.96.0.0/24"}, Endpoint: "10.0.2.2:51820", Keepalive: 25}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	conf := good.Config("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=")
	for _, want := range []string{"Address = 100.96.0.2/24", "AllowedIPs = 100.96.0.0/24", "Endpoint = 10.0.2.2:51820",
		"PersistentKeepalive = 25"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config missing %q:\n%s", want, conf)
		}
	}
	if confKey(conf) != "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=" || confAddress(conf) != "100.96.0.2/24" {
		t.Error("reading the config back")
	}
	for name, bad := range map[string]func(*WireGuard){
		"address outside":  func(w *WireGuard) { w.Address = "192.168.1.5/24" },
		"allowed all":      func(w *WireGuard) { w.Peers[0].AllowedIPs = []string{"0.0.0.0/0"} },
		"endpoint newline": func(w *WireGuard) { w.Peers[0].Endpoint = "host:1\\nPostUp = rm -rf /" },
		"endpoint no port": func(w *WireGuard) { w.Peers[0].Endpoint = "host" },
		"bad key":          func(w *WireGuard) { w.Peers[0].PublicKey = "x\\nPostUp=evil" },
		"bad join key":     func(w *WireGuard) { w.JoinKey = "short" },
		// Valid base64 to a lenient decoder, which skips newlines.
		"key with newline":      func(w *WireGuard) { w.Peers[0].PublicKey = "HIgo9xNzJMWLKASShiTqIybxZ0U3\nwGLiUeJ1PKf8ykw=" },
		"saved key with return": func(w *WireGuard) { w.SavedKey = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=\r" },
	} {
		w := good
		w.Peers = []Peer{good.Peers[0]}
		w.Peers[0].AllowedIPs = []string{"100.96.0.0/24"}
		bad(&w)
		if w.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestWireGuardAPI(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := context.Background()
	if _, err := c.ApplyWireGuard(ctx, WireGuard{Address: "8.8.8.8/32"}); err == nil || host.wg != nil {
		t.Error("an invalid request must not reach the host")
	}
	pub, err := c.ApplyWireGuard(ctx, WireGuard{Address: "100.96.0.1/24", ListenPort: WireGuardPort})
	if err != nil || pub != "cHVibGlj" || host.wg.ListenPort != WireGuardPort {
		t.Errorf("pub %q, err %v", pub, err)
	}
}

func TestServersRule(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: 8471, Proto: "tcp", For: trust.Servers, Reason: "roostack servers"})
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fw.Ruleset(), `iifname "roostack" tcp dport 8471 accept`) {
		t.Errorf("ruleset:\n%s", fw.Ruleset())
	}
	fw.Allow[len(fw.Allow)-1].Source = "100.96.0.2"
	if fw.Validate() == nil {
		t.Error("a source is only for a guest lab's rule")
	}
}

func TestInterfaces(t *testing.T) {
	for _, name := range []string{"roostack", "roostack-guests", "roostack-st1", "roostack-st9"} {
		if !ValidInterface(name) {
			t.Errorf("%s should be valid", name)
		}
	}
	for _, name := range []string{"", "eth0", "roostack-st0", "roostack-st10", "roostack-guests2", "../x"} {
		if ValidInterface(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
	// Guest links use their own range, never the servers' one.
	guest := WireGuard{Interface: GuestsInterface, Address: "100.101.23.1/24", ListenPort: GuestsPort,
		Peers: []Peer{{PublicKey: "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", AllowedIPs: []string{"100.101.23.2/32"}}}}
	if err := guest.Validate(); err != nil {
		t.Error(err)
	}
	guest.Address = "100.96.0.1/24"
	if guest.Validate() == nil {
		t.Error("a guest link can't use the servers' range")
	}
	lab := WireGuard{Address: "100.101.23.1/24"}
	if lab.Validate() == nil {
		t.Error("the lab network can't use the guests' range")
	}
	if hs := parseHandshakes("abc= 1790000000\nxyz= 0\n"); hs["abc="] != 1790000000 || hs["xyz="] != 0 || len(hs) != 2 {
		t.Errorf("handshakes = %v", hs)
	}
}

// The hub forwards only the listed ports between servers; nothing else
// passes through Roostack's network.
func TestForwardRules(t *testing.T) {
	fw := DefaultFirewall()
	fw.Forward = []Rule{{Port: 8000, Proto: "tcp", For: trust.Servers, Reason: "backup stores"}}
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	rs := fw.Ruleset()
	for _, want := range []string{"chain forward {", "policy accept;",
		`iifname "roostack" oifname "roostack" ct state established,related accept`,
		`iifname "roostack" oifname "roostack" tcp dport 8000 accept`, `iifname "roostack" drop`, `oifname "roostack" drop`} {
		if !strings.Contains(rs, want) {
			t.Errorf("missing %q:\n%s", want, rs)
		}
	}
	for _, bad := range []Rule{{Port: 8000, Proto: "udp", For: trust.Servers}, {Port: 8000, Proto: "tcp", For: trust.Guests},
		{Port: 8000, Proto: "tcp"}, {Port: 8000, Proto: "tcp", For: trust.Anyone}} {
		if (Firewall{SSHFrom: FromLAN, Forward: []Rule{bad}}).Validate() == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	if !strings.Contains((WireGuard{Address: "100.96.0.1/24", Forward: true}).Config("k"), "PostUp = echo 1 > /proc/sys/net/ipv4/conf/%i/forwarding") {
		t.Error("forwarding should come back at boot")
	}
	if (WireGuard{Interface: GuestsInterface, Address: "100.97.0.1/24", Forward: true}).Validate() == nil {
		t.Error("only Roostack's network forwards")
	}
}

// Direct links replace only the marked peers: the link to the controller
// stays, and ListenPort follows Listen.
func TestLinks(t *testing.T) {
	ctrl := "Y29udHJvbGxlcmNvbnRyb2xsZXJjb250cm9sbGVyMTI="
	b, c := "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI=", "Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2M="
	node := WireGuard{Address: "100.96.0.3/24", Peers: []Peer{{PublicKey: ctrl, AllowedIPs: []string{"100.96.0.0/24"},
		Endpoint: "lab.example.com:51820", Keepalive: 25}}}.Config("cHJpdmF0ZQ==")

	l := Links{Listen: true, Peers: []Peer{{PublicKey: b, AllowedIPs: []string{"100.96.0.2/32"}, Endpoint: "10.0.0.2:51820", Keepalive: 25}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	conf, err := l.apply(node)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ListenPort = 51820", "PublicKey = " + ctrl, "Endpoint = lab.example.com:51820",
		LinkMarker + "\n[Peer]\nPublicKey = " + b, "Endpoint = 10.0.0.2:51820"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q:\n%s", want, conf)
		}
	}
	// Changed: b goes, c comes, no longer listening; the controller stays.
	conf, err = Links{Peers: []Peer{{PublicKey: c, AllowedIPs: []string{"100.96.0.4/32"}}}}.apply(conf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conf, b) || strings.Contains(conf, "ListenPort") || !strings.Contains(conf, "PublicKey = "+c) ||
		!strings.Contains(conf, "PublicKey = "+ctrl) || strings.Count(conf, "[Peer]") != 2 || strings.Count(conf, LinkMarker) != 1 {
		t.Errorf("after the change:\n%s", conf)
	}
	// None: back to just the controller.
	if conf, _ = (Links{}).apply(conf); strings.Count(conf, "[Peer]") != 1 || strings.Contains(conf, c) || !strings.Contains(conf, "PrivateKey") {
		t.Errorf("without links:\n%s", conf)
	}

	if _, err := (Links{Peers: []Peer{{PublicKey: ctrl, AllowedIPs: []string{"100.96.0.9/32"}}}}).apply(node); err == nil {
		t.Error("a link can't take over the controller's peer")
	}
	for _, bad := range []Peer{{PublicKey: b, AllowedIPs: []string{"100.96.0.0/24"}}, {PublicKey: b, AllowedIPs: []string{"8.8.8.8/32"}},
		{PublicKey: b, AllowedIPs: []string{"100.96.0.2/32", "100.96.0.3/32"}}, {PublicKey: b, AllowedIPs: []string{"100.96.0.2/32"}, Endpoint: "x y:1"}} {
		if (Links{Peers: []Peer{bad}}).Validate() == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
}

func TestLinksRequest(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := context.Background()
	if err := c.ApplyLinks(ctx, Links{Peers: []Peer{{PublicKey: "eA==", AllowedIPs: []string{"100.96.0.2/32"}}}}); err == nil || host.links != nil {
		t.Error("an invalid request must not reach the host")
	}
	l := Links{Listen: true, Peers: []Peer{{PublicKey: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI=", AllowedIPs: []string{"100.96.0.2/32"}}}}
	if err := c.ApplyLinks(ctx, l); err != nil || host.links == nil || !host.links.Listen {
		t.Errorf("err %v, links %+v", err, host.links)
	}
}
