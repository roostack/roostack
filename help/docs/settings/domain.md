# Your lab's address

Your services each get a web address, such as `photos.smiths.roost.ac` or
`photos.smith.family`, with a secure (HTTPS) certificate. Until your lab
has an address, its services run but can't be reached on the web: each
says **Waiting for an address**.

There are two ways to give your lab an address, in the setup wizard or in
**Settings → Your lab's address**:

A free roost.ac address
:   Nothing to buy or set up. You'll pick a name, such as `smiths`; your
    services will be at `photos.smiths.roost.ac`, and Roostack itself at
    `roost.smiths.roost.ac`. It uses Roostack Cloud, with a free account: see
    [Roostack Cloud](cloud.md) for how the lab is linked to it.

Your own domain
:   A domain you own, such as `smith.family`: services at
    `photos.smith.family`, and Roostack at `roost.smith.family`.

Either way, everyone's own account (where they sign in and find their
services) is at `my.` in front of the address, and you choose what the
address on its own shows: see [The address on its own](#the-address-on-its-own).

You can switch later. When you do, every service's address will change, so
the old addresses will keep working for 30 days while you update bookmarks
and apps: see [Changing the address](#changing-the-address).

## Your own domain

A **domain** is a name you own on the internet, such as `smith.family`.
You rent it from a company (a *registrar*) by the year, usually for a few
dollars or pounds. The company that runs its **DNS** (the internet's
address book) is your **DNS provider**; it's usually the same company.

**Buying one:** choose a registrar whose DNS Roostack supports (the list
below) so that Roostack can manage everything for you. Cloudflare and
Porkbun are good, inexpensive choices. Search for a name, buy it, and keep
the account safe: the domain is how everyone reaches your services.

**Already have one elsewhere?** You can keep it where it is and move only
its DNS to a supported provider (Cloudflare's is free): the registrar's
own help explains how to change a domain's *nameservers*.

**Then, in Roostack:** choose **Your own domain**, enter the domain, your
email address, and a login (an API token or key) for its DNS provider. The
page shows what each provider needs and where to get it.

## The address on its own

What someone gets when they open `smith.family` (or `smiths.roost.ac`) on
its own, without a service's name in front of it, is your choice, under
**What smith.family shows** on **Settings → Your lab's address**:

Nothing from Roostack
:   Roostack won't set a DNS record for it, so your own website or anything
    else there will keep working. This is the default for your own domain.

Your account
:   It will go to `my.smith.family`, where everyone signs in and finds their
    services. This is the default for a roost.ac address.

A service
:   It will go to one of your lab's services, such as a blog.

Another address
:   It will go to any web address, such as a website you have elsewhere.

A simple page
:   A page in your lab's look (its name and logo, from **Settings → Name
    and look**) with a heading, a few lines of text, and links to the
    services you pick. Leave a blank line between paragraphs.

You also choose who can open it: your home network only, or anyone on the
internet, for a website's visitors. If it's public, CrowdSec will turn
away known attackers, as it does for a public service.

Any choice but the first needs a DNS record for the address itself.
Roostack will add it and keep it pointing at your lab. If there's already
one that Roostack didn't create (your website's, say), Roostack will leave
it alone and offer **Take over this record…**, which asks you to confirm
first, since whatever uses that record now will stop working there.

??? note "For technical users"

    Caddy serves the address itself with its own certificate, beside the
    wildcard one. The redirects are temporary (302), so browsers won't
    remember them if you change your mind. The simple page is plain files
    Roostack writes to `/var/lib/roostack/caddy/domain` whenever you save
    it, the lab's look changes, or Roostack applies a change to the lab's
    services (so that a service's new address reaches it), served with a
    content security policy that allows no scripts. The choice is kept in
    `lab.yaml` under `domain_itself`.

    Through Roostack Cloud's relay on your own domain, public addresses are
    CNAMEs to the relay, but DNS allows no CNAME at the top of a domain. If
    your domain's DNS provider flattens CNAMEs there (Cloudflare does), add
    that record by hand; otherwise keep the address on its own home-only.

## Secure addresses (HTTPS)

Every address gets a secure certificate, so your browser shows the padlock
and nobody on your network can read what's sent. Roostack gets one
certificate for your whole domain and renews it by itself.

To get it, Roostack proves to the certificate company that the domain is
yours, by briefly adding a record at your DNS provider. That's why it asks
for a **login (an API token or key) for your DNS provider**. It checks the
login works before saving it.

## Supported DNS providers

- Cloudflare *(recommended, and free)*
- Amazon Route 53
- Porkbun
- Gandi
- GoDaddy
- Namecheap
- Spaceship

The domain page shows what each one needs and where to get it.

!!! warning "Namecheap"

    Namecheap's system rewrites every record on your domain to change one.
    Roostack can use it, but will ask you to accept the risk first. Moving
    your DNS to Cloudflare (free) avoids it.

**If your provider isn't listed,** move your domain's DNS to one that is:
Cloudflare's is free. The domain will stay where you bought it; only the
records will move. Cloudflare will copy your existing records when you add
the domain, then show two name servers to set at the company you bought it
from. Check your email and any other records came across before you
switch.

## Where your lab is

**Settings → Where it is and DNS** says whether your lab is **at home**,
behind a router, or **on the internet**: a cloud server (a VPS) with a
public address of its own. The setup wizard asks the same question and
suggests an answer from the server's addresses.

At home
:   Services are for your home network (or a VPN into it). Making one
    public means forwarding ports on your router. Roostack's own pages stay
    private.

On the internet
:   There's no home network, so services start public, protected by
    CrowdSec, and private ones are only reachable through an SSH tunnel
    ([how](../services/public.md#private-on-a-cloud-server)). Roostack's own
    pages are at `roost.<your domain>`, behind your password and a code,
    unless you keep them to an SSH tunnel. One DNS record, `*.<your
    domain>`, points at the server's public address.

Changing it will restart Roostack.

## Reaching your services from home

Your services are private: only for devices on your home network. So that
those devices find them, Roostack can keep a record at your DNS provider
pointing `*.smith.family` at your server's address at home. It only ever
changes records it created itself.

**Settings → Where it is and DNS** shows whether devices at home can reach
your services, and fixes are suggested when they can't:

The record exists, but your home network's DNS won't return it
:   Usually your router's **DNS rebinding protection**, which blocks
    answers that point at home addresses. Allow your domain in the router's
    settings (often called *rebind protection* or *private domain
    exceptions*).

Your home network's DNS answers with another address
:   Something on your network overrides your domain, such as Pi-hole,
    AdGuard, or a router setting. Point it at your server there, or remove
    the override.

The record isn't visible yet
:   DNS changes can take a few minutes, and Roostack will check again by
    itself.

**Give your server a fixed address** in your router (a *DHCP reservation*)
so that its address at home doesn't change.

If a record with that name already exists (one you made before), Roostack
will leave it alone and offer **Take over this record…**.

??? note "For technical users"

    Certificates: one wildcard certificate (`*.<domain>`) from Caddy's
    default issuers (Let's Encrypt, with ZeroSSL as a fallback), through
    the DNS-01 challenge, using the provider's Caddy
    plugin. The credentials are Podman secrets, passed to Caddy's container
    as environment variables. Certificates are in
    `/var/lib/roostack/caddy-data`.

    Records: Roostack keeps an `A` record, `*.<domain>` → this server's LAN
    address, through the provider's API, and records which records it
    created (it adopts an identical one). It checks the name through the
    home network's resolver and through `1.1.1.1` to tell a missing record
    from a router blocking it. Checks run every 30 minutes.

## DNS for your devices

A DNS server finds the address of each name a device looks up. Under
**DNS for your devices** on **Settings → Where it is and DNS**, choose
which one the lab uses:

- **Roostack's own** (recommended): it sends your devices straight to the
  server that runs each service, and passes every other name on to your
  usual DNS. See [Roostack's own DNS server](#roostacks-own-dns-server).
- **A DNS server among your services** (the default): once you install
  one, such as [Pi-hole](https://pi-hole.net) or [AdGuard
  Home](https://adguard.com/adguard-home/overview.html), both in the
  catalog, Roostack will use it by itself (the first by name, if you install
  both on different servers), and it can leave out ads and trackers too.
  Until you install one, Roostack's own is answering. The card says which
  one, and its address.
- **Each device's own**: usually your internet provider's, through your
  router. Roostack will leave them alone.
- **The one at an address you choose**: a Pi-hole elsewhere on your home
  network, or a public one such as `9.9.9.9`.

The lab's VPN devices will use it wherever they are, once you download
their files again, as long as they can reach it: a home address needs a
device that sends the home network through the VPN. A device whose file
doesn't name a DNS server keeps using its own.

For the other devices at home, set it in your router as the DNS server it
gives out (Roostack can't change the router). Pi-hole and AdGuard Home
also answer your lab's own names with your server's home address, so
devices that use them
reach your services directly, public ones too, even while the internet is
down.

Tick a server under **Servers that use it too** to have its own lookups
use it as well. That's only worth it for a server whose own lookups should
leave out ads too, or reach the lab's services directly. It will ask the
lab's DNS server first, and keep its own for when that one doesn't answer.
It will also trust that DNS server for every name it looks up, including
the time servers it sets its clock from, so tick a server only if you
trust the DNS server as much as the server itself. The server running
Roostack's own DNS server can't be ticked, since it would ask itself.

??? note "For technical users"

    A VPN device's file gets a `DNS =` line. A server you tick gets a
    systemd-resolved drop-in, `/etc/systemd/resolved.conf.d/roostack.conf`,
    with `DNS=<address>:<port>` (Pi-hole's port on its server, 5053), which
    `roostack-agent` writes and removes, restarting systemd-resolved. A
    server without systemd-resolved is left alone, and the apply's log says
    so. The choice is `resolver:` in `lab.yaml`. Pi-hole's names come from
    `FTLCONF_misc_dnsmasq_lines` (`address=/<domain>/<home address>`), and
    AdGuard Home's from a block list Roostack writes,
    `work/userfilters/roostack.txt` in its data (`$dnsrewrite` rules),
    which its install hook adds, and which it reads again at each start.

## Roostack's own DNS server

Roostack's own DNS server answers each of your lab's names with the home
address of the server that runs it, and passes every other name on to the
DNS servers your home network already gives out, usually your router's.
Devices that use it reach your services straight from home, public ones
too, rather than through your router's way back in, and keep reaching
them while the internet is down. It answers only the home network and the
lab's VPN devices.

It runs on the lab's controller while that's at home. When the
controller is on the internet, it runs on a server at home, which you can
choose on the card (the first by name, unless you choose).

### Setting up your router

Every device at home will use it once your router gives it out:

1. Open your router's settings page. Its address is often printed on the
   router, such as `192.168.1.1`.
2. Find the settings for the home network, often called **LAN** or
   **DHCP**. Don't change the router's own DNS, which is often under
   **Internet** or **WAN**.
3. Set the first DNS server to the address the card shows, such as
   `192.168.1.10`, and the second to a public one, such as `9.9.9.9`.
4. Save. Your devices will use it once they reconnect: turn Wi-Fi off and
   on, or wait a day.

The second server is there for when the lab's is off. Some devices ask
both, or whichever answers first, which is fine: the public one sends
them to the same places, apart from parts of services kept to home on a
server on the internet, which open only through the lab's DNS.

If your router also gives out a DNS server for IPv6, set that to the
lab's IPv6 address too, or turn it off: phones often prefer it, and would
never ask the lab's.

Some routers, often those from internet providers, don't let you change
this. Then set the DNS server on each device instead, or use the
[lab's VPN](vpn.md), which sets it by itself.

Your server's home address mustn't change, so reserve it in your router
(often called a DHCP reservation or static lease), as your lab's address
already needs.

### Is this device using it?

**Your services** on Your account says whether the device you're using
asks the lab's DNS, and how to set it up if not.

??? note "For technical users"

    It's `roostack-dns.service`, the same binary as Roostack
    (`roostack dns`), running as the `roostack` user with no access to
    Roostack's state or secrets. Roostack writes the names it answers to
    `/var/lib/roostack/dns/names.json` on the server that runs it, and
    `roostack-dns.path` starts the service while that file is there; on
    other servers Roostack deletes it, and the service stops. It listens on
    port 5055, UDP and TCP, and the firewall sends port 53 from the home
    network and the lab's VPN devices to it. Its log is `journalctl -u
    roostack-dns`.

    It answers `A` questions for the lab's names with a TTL of 60
    seconds, `AAAA` and `HTTPS` questions for them with nothing, and
    `use-application-dns.net` with NXDOMAIN, which keeps Firefox on the
    network's DNS. Every other question goes, as it came, to the
    nameservers in `/run/systemd/resolve/resolv.conf` (else
    `/etc/resolv.conf`), leaving out the server's own addresses and any that
    sends a test question back to it. With none left, it asks `9.9.9.9`, or
    the address you choose on the card. It refuses anyone outside the home
    network and the VPN's range. The home addresses that asked in the last
    week are counted in `/var/lib/roostack-dns/clients.json`.

    The check on Your account loads a picture from `check-<random>.<domain>`,
    a name nobody has asked for before, and asks the DNS server whether it
    was asked for it.

## Changing the address

Choose **Change the address…** on **Settings → Your lab's address**. When
the address changes, every service's address will change with it, so for
**30 days** the old addresses will keep working too, exactly as before.
Meanwhile:

- the home page will say the address changed and until when the old one
  works;
- **Settings → Your lab's address** will list every old and new address so
  that you can update bookmarks and the server address in apps on phones
  and computers (such as Bitwarden's or Immich's);
- each service's page will show its old address.

Passkeys and security keys made for the old address won't work at the new
one: add new ones in each app. Settings inside apps that include the
address (such as Vaultwarden's) will change by themselves.

When you've updated everything, **Stop the old addresses now** will end it
early. Otherwise it will end by itself after 30 days.

**The old domain's DNS records.** During the 30 days, the records Roostack
set under the old domain will follow your home's addresses as the new
domain's do. A service you make private will lose its old record too. When
the old address stops, Roostack will delete those records using the old DNS
provider's login it kept, and then delete that login. It will delete only
the records it set, and only those nobody has changed since: one you've
changed is yours.

If the old provider refuses (you deleted the old login's API key, say),
**Settings → Your lab's address** will list the records still there, with
what the provider said, and Home will say so too. Roostack will keep the
login and try again every hour. To finish it yourself, delete the records
at the old DNS provider, then choose **I've deleted them**, and Roostack
will stop trying and delete the login.

Only one old address is kept: if you change the address again within the
30 days, the address before will stop at once, and Roostack will delete its
records first.

**From one roost.ac name to another,** Roostack Cloud will keep the old
name working for those 30 days too (its records, and its certificate's
renewals), then let it go, records and all: it will stay kept for your
account for a year so that nobody else can take it. If the new name can't
be set up, your lab will keep the old one as it was.

