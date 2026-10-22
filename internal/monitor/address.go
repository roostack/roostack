package monitor

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/usererr"
)

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)

// validHost says whether h is an IP address or a host name.
func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return len(h) <= 253 && hostnameRe.MatchString(h)
}

// ParseAddress reads the address of someone's own check (SPEC.md §6.15):
// a web address (http:// or https://) to ask, a name or IP address with a
// port to connect to, or one without to ping. The check returned has URL,
// TCP or Ping set; an error says what's wrong, for the form.
func ParseAddress(addr string) (Check, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return Check{}, usererr.New("Type the address to check, e.g. https://nas.local or 192.168.1.1.")
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !validHost(u.Hostname()) || u.User != nil {
			return Check{}, usererr.New("A web address starts with http:// or https:// and a name or IP address, e.g. https://nas.local:5001.")
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return Check{}, usererr.New("A port is a number from 1 to 65535.")
			}
		}
		u.Fragment = ""
		if u.Path == "" {
			u.Path = "/"
		}
		return Check{URL: u.String()}, nil
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Check{}, usererr.New("A port is a number from 1 to 65535, after a colon: printer.local:631.")
		}
		if !validHost(host) {
			return Check{}, usererr.New("That isn't a name or IP address Roostack can check.")
		}
		return Check{TCP: net.JoinHostPort(host, port)}, nil
	}
	host := strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
	if !validHost(host) {
		return Check{}, usererr.New("That isn't a name or IP address Roostack can check: a web address starts with http:// or https://.")
	}
	return Check{Ping: host}, nil
}
