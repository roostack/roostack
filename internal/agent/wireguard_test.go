package agent

import (
	"context"
	"os"
	"strings"
	"testing"
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

func TestInterfaceRule(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: 8471, Proto: "tcp", Iface: WireGuardInterface, Reason: "roostack servers"})
	if err := fw.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fw.Ruleset(), `iifname "roostack" tcp dport 8471 accept`) {
		t.Errorf("ruleset:\n%s", fw.Ruleset())
	}
	fw.Allow[len(fw.Allow)-1].Iface = "eth0"
	if fw.Validate() == nil {
		t.Error("only Roostack's interface is allowed")
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
