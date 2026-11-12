# The lab's VPN

The lab's VPN lets the lab's people reach its services from anywhere, on
their own phones and laptops, as if they were at home. Each person adds
their own devices on **Your account → VPN devices**, and each device
reaches only the services its person can use.

It's off until the Owner turns it on, on **Settings → VPN**.

## Turning it on

On **Settings → VPN**, turn on **Let people add devices**. Your lab needs
an address first, since devices connect to it.

**Devices connect to** one of your lab's servers, on UDP port 51822:

- **A cloud server in your lab,** if you have one, which the internet can
  reach already: recommended, as there's nothing to set up on your router.
  Devices pass through it to your lab at home over Roostack's own network.
- **The server that runs your lab, at home,** at `link.smith.family`, which
  Roostack will keep pointing at your home's internet address, even when it
  changes. Forward UDP port 51822 on your router to the server, as you
  would for a game or a camera. Roostack can't check a forward from inside
  your home, so once someone has added a phone, turn its Wi-Fi off and turn
  the VPN on in the WireGuard app: its **Last connected** on
  **Settings → VPN** will change when it reaches the lab.

If your lab runs on a cloud server itself, devices connect to it.

Changing the server will stop every device until it gets new settings:
on **Your account → VPN devices**, choose **Change** for each one and tick
**Give it new settings**.

You also choose:

Devices each person can add
:   5 by default. Leave it empty for no limit. A limit means a lost or
    forgotten device gets noticed when someone wants another.

The home network
:   Whether a device may also reach your home's other devices, such as a
    NAS or a printer. Off by default.

Everything
:   Whether a device may send all its internet traffic through the lab,
    which is safer on public Wi-Fi. It will use your lab's internet
    connection, and what it does online will appear to come from your
    home's address. Off by default.

**Settings → VPN** lists everyone's devices, when each last connected,
and **Remove** for any of them. A device that hasn't connected for 90
days is marked, since it may be lost. Removing a person removes their
devices too, and you'll get an email when someone adds one, if you get
security emails.

## Adding a device

On **Your account → VPN devices**, choose **Add a device…**, then:

1. Give it a name, such as "Alex's phone".
2. Choose what it should reach: **The lab only** (its other internet
   traffic will go the usual way), or the home network or everything, if
   the Owner allows them.
3. Owners and Admins can also tick **Roostack's pages**, and Owners
   **SSH to the lab's servers**, for that device only.
4. Enter your two-factor code if you have two-factor turned on.

Roostack will show the device's settings once, as a QR code and a file:

1. Install a WireGuard app on the device. [WireGuard's own](https://www.wireguard.com/install/)
   is free for iPhone and iPad, Android, Windows, macOS, and Linux, and any
   other WireGuard app works too.
2. In the app, add a tunnel from a QR code and scan it. On a laptop, add
   it from the file instead.
3. Turn the tunnel on.

**You'll only see the settings once.** They hold the device's private key,
which Roostack doesn't keep, so if you lose them, add the device again
and remove the old one.

**Changing a device:** its name, Roostack's pages, and SSH change straight
away. Changing what it reaches gives it new settings to add again, since
those are part of the settings on the device. **Give it new settings**
does the same without changing anything else, for a device whose settings
were lost, or when the VPN has moved to another server.

## What a device reaches

Each device has its own address, so Roostack knows whose it is:

- **Services:** those its person can use. A service with single sign-on
  is for the people chosen on its page; one without is for everyone in the
  lab, as it is at home. **Your account** is open to every device.
- **Roostack's pages:** only from a device that has them ticked.
- **SSH:** only from an Owner's device that has it ticked, and only while
  someone can log in over SSH at all (**Settings → Remote access**).
- **Anything else at home:** only with **The lab and the home network**.

The lab's addresses (`photos.smith.family`) work exactly as at home: the
device sends only the lab's own address through the VPN, so it doesn't
clash with a café's network using the same addresses as yours.

??? note "For technical users"

    Roostack runs a WireGuard interface, `roostack-people`, on UDP 51822,
    on the server devices connect to, with addresses in `100.96.128.0/17`,
    one for each device; the server's is `100.96.128.1`. When that's a
    cloud server, it passes devices' traffic on to the controller over
    Roostack's network (`roostack`), keeping their addresses, so the
    controller's firewall and Caddy still see each device. Its settings are in
    `/etc/wireguard/roostack-people.conf`, and its key is backed up with the
    lab's other WireGuard keys, so devices keep working after a restore.

    Devices are never treated as the home network. Roostack's firewall
    (`nft list table inet roostack`) lets them reach Caddy, and SSH or a
    service's home-only ports only from the addresses allowed them; it
    forwards only the traffic of devices allowed the home network or
    everything, and gives it the server's address (masquerade). For that,
    it turns forwarding on for the interfaces the answers come back in on;
    where it was off before, only answers are forwarded from there, so the
    server doesn't route anything else for your network, and it goes back
    off when the VPN no longer needs it. Caddy
    lets each device through to a private service only if its address is
    listed for it.

    Roostack makes each device's key pair when it's added, puts the
    private key only in the settings it shows, and keeps the public key in
    its database. The settings' `AllowedIPs` is the address the lab's names
    point at (`/32`), plus the home network's ranges or `0.0.0.0/0` when
    allowed. A device could edit that, which is why the firewall and
    Caddy check every connection.
