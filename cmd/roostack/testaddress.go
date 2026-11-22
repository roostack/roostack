package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
)

const testAddressUsage = `Usage:
  sudo roostack test-address [name]
  sudo roostack test-address -off

Gives a throwaway lab, such as a test VM, an address of its own without a
domain, a DNS provider, or Roostack Cloud: <name>.localhost (lab.localhost
when no name is given), so that services are at outline.lab.localhost, the
sign-in page at my.lab.localhost, and Roostack's pages at
roost.lab.localhost. Names ending in .localhost always mean the computer
they're opened on, so only the server itself can reach them (or a computer
whose port 443 is forwarded to it), and Caddy signs their certificates with
its own certificate authority, whose root it keeps at
/var/lib/roostack/caddy-data/caddy/pki/authorities/local/root.crt.

It's only for a lab without an address: it won't replace a real one.
-off takes the test address away again. Run it on the lab's controller as
root: the change will be saved as the roostack user, and root will then
restart Roostack so that Caddy and every service use it.
`

// testAddressName is the part before .localhost: one DNS label.
var testAddressName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// testAddressCmd gives a throwaway lab a test address, or takes it away
// (SPEC.md §6.6, "A test address for throwaway labs"). It's only ever set
// here, never from Roostack's pages.
func testAddressCmd(args []string) error {
	fs := flag.NewFlagSet("test-address", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), testAddressUsage) }
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	off := fs.Bool("off", false, "take the test address away")
	fs.Parse(args)
	name := "lab"
	switch {
	case fs.NArg() > 1 || (*off && fs.NArg() > 0):
		fs.Usage()
		return errors.New("test-address: give one name, or -off")
	case fs.NArg() == 1:
		name = fs.Arg(0)
	}
	if !testAddressName.MatchString(name) {
		return fmt.Errorf("test-address: %q isn't a name Roostack can use: use lowercase letters, digits, and hyphens, like smiths", name)
	}
	say := func(format string, args ...any) { fmt.Printf("==> "+format+"\n", args...) }

	// As root: the change is made as the roostack user (asroostack.go),
	// then root restarts Roostack.
	if os.Geteuid() == 0 {
		if err := runAsRoostack(append([]string{"test-address"}, args...)); errors.Is(err, nothingChanged) {
			return nil
		} else if err != nil {
			return err
		}
		if exec.Command("systemctl", "is-active", "--quiet", "roostack").Run() != nil {
			say("Roostack isn't running: it will use the address when it next starts.")
			return nil
		}
		if out, err := exec.Command("systemctl", "restart", "roostack").CombinedOutput(); err != nil {
			return fmt.Errorf("restarting Roostack: %w: %s", err, out)
		}
		say("Roostack has restarted, and it's applying the change now. Caddy will start once its image has downloaded (%s).",
			config.Default().Paths.CaddyImageLog())
		return nil
	}
	if !fromRoot() {
		return errors.New("test-address: run it as root, with sudo, so that Roostack can be restarted afterwards")
	}

	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	// Only the controller's Caddy serves the lab's addresses (SPEC.md
	// §10.2).
	if m, ok, err := labnet.LoadMembership(lab.Paths.StateDir); err != nil {
		return err
	} else if ok {
		m.Apply(&lab)
	}
	if lab.IsNode() {
		return errors.New("test-address: this server isn't the lab's controller: run it on the controller, which keeps the lab's settings")
	}
	if *off {
		if !lab.TestAddress() {
			say("This lab has no test address, so there's nothing to take away.")
			return nothingChangedIfRoot()
		}
		if err := saveTestAddress(*labPath, "", config.DNS{LANIP: lab.DNS.LANIP}); err != nil {
			return err
		}
		say("%s isn't the lab's address any more: the lab has no address now.", lab.Domain)
		return nil
	}
	domain := name + config.TestAddressSuffix
	switch {
	case lab.Domain == domain && lab.TestAddress():
		say("The lab's address is already %s.", domain)
		return nothingChangedIfRoot()
	case lab.Domain != "" && !lab.TestAddress():
		return fmt.Errorf("test-address: this lab already has an address, %s, and a test address is only for a lab without one", lab.Domain)
	}
	if err := saveTestAddress(*labPath, domain, config.DNS{Provider: config.ProviderTestAddress, LANIP: lab.DNS.LANIP}); err != nil {
		return err
	}
	say("The lab's address is now %s, a test address with Caddy's own certificates.", domain)
	say("Caddy's root certificate will be at %s once Caddy has started.", lab.Paths.CaddyLocalRoot())
	return nil
}

// saveTestAddress saves the lab's address and its DNS provider, and checks
// the result is a lab Roostack will start with.
func saveTestAddress(labPath, domain string, d config.DNS) error {
	if err := config.Update(labPath, func(l *config.Lab) { l.Domain, l.DNS = domain, d }); err != nil {
		return err
	}
	_, err := config.Load(labPath)
	return err
}
