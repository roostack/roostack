# Make a service public

Services are for your home network (and VPN) unless you choose otherwise.
Making one **public** lets anyone on the internet reach it: handy for
sharing photos with family, or using your password manager away from home
without a VPN.

Only the Owner can make a service public. At home, Roostack's own pages
always stay private.

On a **cloud server** it's the other way round: there's no home network,
so services start public, and so do Roostack's own pages, behind your
password and two-factor code. See [On a cloud server](#on-a-cloud-server).

## Before you do

- Anyone can reach the service's **login page**, and anything it shares
  without a login. Use strong passwords, and two-factor authentication if
  the service has it.
- Keep it up to date: a public service is found and tried by strangers
  within hours.

## Making it public

On the service's page, next to **Reachable from**, choose **Make
public…**. Roostack will explain what will change and ask you to confirm,
then:

1. **Protection.** Roostack will start **CrowdSec**, which spots attacks in
   the web log, blocks the addresses they come from, and refuses requests
   that try [known exploits](#blocking-known-exploits), and it will limit
   how fast anyone can try passwords. The first time, you'll choose whether
   CrowdSec shares attack reports with its community (only attackers'
   addresses and what they tried). Sharing gets you the community's list
   of known attackers in return. You can change this in **Settings →
   Public access**.
2. **DNS record.** Once CrowdSec is running, Roostack will point the
   service's address at your home's public address. If Roostack doesn't
   manage your DNS records, the page will show the record to add yourself.
3. **Your router.** Forward TCP ports 80 and 443, and UDP port 443, to this
   server's home address (the page shows it). Look for "port forwarding",
   "virtual server", or "NAT" in your router's settings. If the server is a
   cloud server rather than at home, skip this. So can a lab whose visitors
   come in through a cloud server (below).
4. **Check from your phone.** Choose **Make a check link**, turn Wi-Fi off
   so your phone uses mobile data, and scan the code. The step will tick
   itself when your phone gets through.

## Through a cloud server, without touching your router

If your lab has a cloud server (see [Add a server](../servers/add.md)),
public visitors can come in through it instead of your router. Choose it
in **Settings → Public access**, under **How people reach your public
services**. Then:

- public addresses will point at the cloud server, so your home's address
  will stay hidden;
- the cloud server will pass each visitor's connection on to your lab over
  the servers' own private network, **still encrypted**: it can't read
  what's sent, and the certificate stays at home;
- your lab will still see who each visitor is, so CrowdSec, password
  limits, and the private parts of each service will work as before;
- if its provider has a firewall, it has to allow TCP ports 80 and 443.

??? note "For technical users"

    The cloud server runs Roostack's Caddy with the layer-4 plugin: it
    reads the name in each TLS handshake, passes connections for public
    names to the controller's port 443 over WireGuard with a PROXY protocol
    (v2) header, and drops any other name. Plain HTTP for those names is
    redirected. The controller's firewall sends the cloud server's
    connections to a listener of Caddy's own (port 8474), which accepts the
    PROXY header only from the cloud server's WireGuard address and counts
    every visitor there as outside, whatever address the header claims, so
    nothing through the cloud server can reach anything private. HTTP/3 (UDP) doesn't pass through the cloud server: browsers
    use HTTP/2. For now one cloud server is the way in for every public
    address.

## Through Roostack Cloud's relay

A lab linked to Roostack Cloud, whose address is its roost.ac name, can
have visitors come in through Roostack Cloud instead: no router changes
and no server of your own. Choose it in **Settings → Public access**. Like
a cloud server of your own, the relay passes connections on **still
encrypted**: Roostack Cloud can't read them, and the certificate stays in
your lab. It's part of a paid plan (by invitation while it's being tried
out).

### With your own domain

If your lab's address is your own domain (such as `lab.example.com`) rather
than a roost.ac name, Roostack Cloud first needs to know the domain is yours,
so that nobody else can send your names through the relay. Roostack will add
a record for that at your DNS provider, `_roostack.lab.example.com` (TXT),
and point each public address at Roostack Cloud with a CNAME record. If
Roostack doesn't manage your DNS records, **Settings → Public access** will
show the records to add yourself, with a **Check now** button.

Until Roostack Cloud finds the record, public addresses won't come through
the relay, and Home will say so. If the record is ever deleted, they'll stop
a day later, and Home will show it as a problem so that you have time to put
it back.

### Visitors by country

While the relay is on, **Settings → Public access → Visitors by country**
can turn visitors away by the country their address is in, before they
reach your lab:

- **Anyone, from any country**: the default.
- **Only visitors from some countries**: for a family lab, often just your
  own. The strongest choice, but your own phone abroad will be turned away
  too.
- **Everyone except visitors from some countries.**

It's a rough filter: an address's country isn't always known or right, and
someone turned away gets no message; the site just won't load. Before you
travel, use **Let everyone in** for a day, a week, two weeks, or a month:
the limit will come back by itself afterwards.

Addresses your lab's CrowdSec bans are turned away by the relay too, for
your lab only, so an attacker it caught doesn't keep using your bandwidth.

### Turned off in Roostack Cloud

Your Roostack Cloud account's page can turn the relay off, even while your
lab is off (to stop what you pay for, say). Your lab will then hear it from
Roostack Cloud within a few minutes: **Settings → Public access** will say
when it was turned off, and Home will say public services aren't coming
through the relay. Their addresses will point at your home's own address
again meanwhile.

Your lab never turns it back on by itself. When you want it again, choose
**Turn the relay on** in **Settings → Public access**.

If Roostack Cloud has **suspended** the relay (after an abuse report),
the page will say why, and there'll be no button: Roostack Cloud emailed
the account's Owner how to appeal.

### Visitors through the relay

While the relay is on, **Settings → Public access** (or, for Admins,
**Services → Visitors through the relay**) shows how many people visited
today, in the last 7 or 30 days, or the last year: visitors, connections,
data, where visitors are, and connections turned away and why. They're
counts only: Roostack Cloud keeps no record of who visited. A visitor is
one address, so a family behind one home connection counts once, and
someone who visits on several days counts once each day.

??? note "For technical users"

    The relay's edges look up each visitor's address in DB-IP's free
    country database (IP geolocation by DB-IP), updated monthly, and close
    the connection before passing anything on; an address it can't place
    counts as from no country, so a list of countries let in turns it
    away. The controller sends CrowdSec's own bans (its decisions and
    those added with `cscli`, not the community's lists) to Roostack Cloud
    every 5 minutes, at most 1,000; the edges drop them an hour after the
    last time, if the lab goes quiet.

## If it doesn't work

If the check link doesn't open, look at these in order. The page ticks the
ones Roostack can check itself.

1. **Roostack's own firewall** lets web traffic in (TCP 80 and 443, UDP
   443). It always does.
2. **Your router** forwards those ports to this server (or, on a cloud
   server, your provider's firewall allows them). Roostack can't see this
   from inside.
3. **Public DNS** points the address at your lab. Roostack checks this
   through a public DNS server.
4. **Your internet connection** accepts incoming connections at all. Some
   don't: they share one public address between many customers (called
   *CGNAT*, common on mobile broadband and some fibre plans). Your router's
   status page shows its internet (WAN) address: if it starts with `100.`,
   or differs from what a "what is my IP" search says, you're behind CGNAT.
   Ask your provider for a public address, or wait for public access
   through a cloud server.

!!! warning "Port 443 already used at home?"

    A router can forward port 443 to only one device. If another device at
    home already has it, Roostack can't share it. Public access through a
    cloud server, which needs no router changes, is planned.

## Blocking known exploits

While a service is public, CrowdSec also checks each request from the
internet before the service gets it. If the request tries an **exploit of
a known vulnerability** in a popular app, such as reading a website's
secret `.env` file, or a bug in an old version of a web app, CrowdSec
refuses it: the visitor gets a page saying *That one ruffled some
feathers*, that the request looked like an attack, and to tell whoever
runs the service if they were only using it as usual. The service never
sees the request. An address that tries two different exploits is blocked
altogether for four hours.

This is sometimes called **virtual patching**: it protects a service from
attacks on vulnerabilities it may have, even before you've updated it. It
doesn't replace updates. It's on for every public service, and for
Roostack's own pages when they're public. Private services aren't
affected, and neither are devices on your home network.

The service's page shows, under **Known exploits**, how many requests were
refused in the last week, and the latest few: what they tried (for example
"PHPUnit RCE (CVE-2017-9841)"), the address they asked for, when, from
where, and the rule that refused them. Roostack never shows or keeps
what was sent with a request. The weekly summary email says how many were
refused, by service.

### If it refuses something it shouldn't

The rules are written for specific exploits, so a refused request is
nearly always an attack. Rarely, one matches something a service really
does. You'd notice that part of the service fails from outside, with
*That one ruffled some feathers* (or just "403 Forbidden", where the app
loads that part in the background), but works at home (requests from
home aren't checked).

1. On the service's page, under **Known exploits**, look for a request
   refused when it failed, from your own address (a website such as
   "what is my IP" shows it). Its line names the rule, such as
   `vpatch-CVE-2024-1234`.
2. If it's yours, turn the blocking off for that service: choose
   **Manage** on the card (or open **Public access** from **Reachable
   from**), then under **Blocking known exploits**, **It refused something
   … needs**, choose **Stop blocking**. Only that service will change, on
   all its public addresses. CrowdSec will still block addresses that
   attack it, and password tries will still be limited. The page will warn
   while it's off, with **Block known exploits** to turn it back on.
3. Tell CrowdSec so that the rule is fixed for everyone: their rules are at
   [github.com/crowdsecurity/hub](https://github.com/crowdsecurity/hub).
   Give the rule's name and what the service was doing, not your
   address. Once a fixed rule reaches Roostack (with an update), turn the
   blocking back on.

If you were blocked altogether (every page of every public service says
*Shooed out of the henhouse* from outside), the block will end after four
hours. A technical user can end it now (below).

??? note "For technical users"

    - Caddy's handler for a public address runs CrowdSec's bouncer, the
      rate limits, then the bouncer's `appsec` handler, for visitors from
      outside only (the same check as private addresses: a visitor
      through an edge or Roostack Cloud's relay is always outside).
    - AppSec runs inside the `roostack-crowdsec` container, on
      `127.0.0.1:8475`, set up by
      `/var/lib/roostack/crowdsec/appsec.yaml` with CrowdSec's
      `crowdsecurity/appsec-default` settings: the rules of the Hub's
      `crowdsecurity/appsec-virtual-patching` and
      `crowdsecurity/appsec-generic-rules` collections. They come from one
      commit of [CrowdSec's Hub](https://github.com/crowdsecurity/hub),
      set in `/var/lib/roostack/crowdsec/config.yaml.local`, and change
      with Roostack's updates. The lists some of CrowdSec's attack
      scenarios use (from `hub-data.crowdsec.net`) can't be pinned that
      way: CrowdSec fetches the latest of them when it starts.
    - The bouncer sends AppSec the request's address, path, headers, and
      the first 1 MiB of its body, over the server's loopback. If AppSec
      doesn't answer within 2 seconds (CrowdSec restarting, say), the
      request goes through, and **Settings → Public access** says
      CrowdSec isn't running.
    - The bouncer hands its refusals to Caddy as errors
      (`enable_caddy_error`), so a visitor gets
      `/var/lib/roostack/caddy/errors/403.html`. A ban and a refused
      request are the same error to Caddy; the page tells them apart by
      `roostack_waf`, which the address's handler sets just before
      AppSec. It never repeats anything from the request.
    - Each refused request is a CrowdSec alert of kind `waf`. In the
      container (`podman exec roostack-crowdsec …` as the `roostack`
      user): `cscli alerts list --kind waf` lists them, `cscli alerts
      inspect -d <id>` shows one, and `cscli decisions delete --ip
      <address>` ends a block.
    - **Stop blocking** sets `no_waf: true` in the service's instance file
      (`/var/lib/roostack/services/<name>.yaml`). A package can't turn it
      off.

## Parts that stay private

Some services have pages that shouldn't be public, such as an admin panel.
Their package marks them, and they stay private (reachable from home, or on
a cloud server only through an SSH tunnel) even when the rest of the
service is public. You can still make one public: tick it when making the
service public, after reading why it's marked, or later under **Parts kept
private** on the same page. The service's page says when one of its links
leads to a private part.

A few services are marked as not safe to make public at all. Roostack will
say why, and you'll have to confirm before it goes ahead.

## On a cloud server

If your lab is on a cloud server (a VPS), tell Roostack in **Settings →
Where it is and DNS**; the setup wizard asks too. Then:

- **Services start public.** Installing one ticks **Anyone on the internet
  can reach it**; untick it to keep it private. Services whose package
  says they aren't safe to make public still start private.
- **Roostack's own pages** are at `roost.<your domain>`, for anyone to
  reach, protected by your password, a code from your authenticator app,
  CrowdSec, and a strict limit on login attempts. You can keep them to an
  SSH tunnel instead.
- **The checklist** asks about your provider's firewall instead of a
  router: many providers (Linode, Hetzner, AWS, DigitalOcean, Oracle Cloud)
  put one in front of your server. Allow TCP ports 80 and 443, and UDP
  port 443, from anywhere. Check from any browser that isn't the server
  itself.
- **DNS:** one record, `*.<your domain>`, points at the server and covers
  every service.

### Private on a cloud server

There's no home network on a cloud server, so a private service is only
reachable **through an SSH tunnel**: a connection from your computer that
ends on the server itself. It's for technical users, and for things you
rarely need, such as an admin page.

1. Tunnel your computer's port 443 to the server's, and leave it open
   (it needs administrator rights on your computer, since 443 is a
   privileged port: `sudo` on macOS and Linux, an administrator PowerShell
   on Windows):

    ```bash
    sudo ssh -L 443:127.0.0.1:443 you@your-server
    ```

2. Make the service's address point at your own computer, in your hosts
   file (`/etc/hosts` on macOS and Linux,
   `C:\Windows\System32\drivers\etc\hosts` on Windows):

    ```text
    127.0.0.1  git.example.com
    ```

3. Open `https://git.example.com` in your browser. Remove the hosts line
   when you're done, or the address won't work without the tunnel.

## Pausing public access

When something is wrong, such as a security problem in something your
services are built with, you can take them off the internet in one step
and put them back just as easily. On **Home**, choose **Pause public
access…**, then what to pause:

- **Everything public**: every service you've made public.
- **By what it's built with**: every public service built with what you
  tick, such as `php`, as its package says. Services installed later that
  are built with the same thing will be paused too.
- **Chosen services**: only the ones you tick. A service's own page has
  **Pause public access to…** as well.

Within a few seconds, visitors from the internet will see a page saying
the service is closed for a while, whichever way they come in. It won't say
why. At home and on your lab's VPN, everything will keep working as it is,
and nothing else will change: the services will keep running, and their
addresses, settings, and certificates will stay as they are. Roostack's own
pages are never paused, so you can always open things again from them.

Owners and Admins can pause, and Roostack will email the Owners straight
away to say who did. A pause will last until an Owner opens it again,
from **Settings → Public access** or the service's page: Home and the
weekly summary will keep saying what's paused until then.

??? note "For technical users"

    - The lab's pauses are in `lab.yaml`, under `public.pause` (`all`, and
      `built_with` by name), each with when and by whom; a service's own is
      `public_pause` in its instance file.
    - A paused service's public routes answer visitors from outside with a
      503 first thing in their `route` block (`error`), setting
      `roostack_blocked`, which makes the error page say it's closed.
      Visitors from home pass through as before, and CrowdSec keeps
      running.
    - Its public package ports are opened to home only at its server's
      firewall, and the sign-in page for services stays public only for
      services that aren't paused.
    - Monitoring still checks it from inside the lab; the public status
      page shows it as paused.
    - When Roostack's pages can't be reached, pause from the controller's
      command line: `sudo roostack public pause -all` (or `-built-with
      php`, or `-service lychee`), `sudo roostack public resume …` to open
      it again, and `sudo roostack public status`. It saves the change
      as the roostack user, records it like the pages do (so the Owners
      will be emailed), and restarts Roostack so that it applies it; if
      Roostack isn't running, it applies it to that server's own services
      itself.

## Making it private again

On the same page, choose **Make private**. The service will be reachable
from home only again (on a cloud server, through an SSH tunnel only). At
home, Roostack will delete the DNS record it made for the address, and the
record for all your services (`*.<your domain>`) will cover it again. It
will delete only the record it made, and only if nobody has changed it
since: one you've changed is yours, and Roostack will leave it alone. If
your DNS provider doesn't answer, **Settings → Public access** will say so,
and Roostack will try again every few minutes. Once nothing is public, and
Roostack's own pages aren't either, CrowdSec will stop.

??? note "For technical users"

    - Making an address public sets `exposure: public` on its route in the
      instance file (`/var/lib/roostack/services/<name>.yaml`); private
      paths you opened are listed under `public_paths`.
    - Caddy's handler for a public address first refuses outside visitors
      on private paths, then asks CrowdSec's bouncer, then applies a rate
      limit per visitor address: 1000 requests per 10 seconds, and 10 a
      minute on the login pages the package names. Then CrowdSec's AppSec
      checks outside visitors' requests ([Blocking known
      exploits](#blocking-known-exploits)).
    - CrowdSec runs as `roostack-crowdsec` while anything is public, reading
      Caddy's JSON access log from `/var/lib/roostack/caddy-logs`. Its
      configuration and database are in `/var/lib/roostack/crowdsec`, and
      its local API is on `127.0.0.1:8473` (AppSec on `127.0.0.1:8475`). `cscli decisions list`, run in
      the container, shows who is blocked.
    - Your home's public address comes from DNS: OpenDNS's
      `myip.opendns.com`, or Google's `o-o.myaddr.l.google.com`.
    - On a cloud server (`network: internet` in `lab.yaml`), private
      addresses accept only connections made on the server itself: Caddy
      sees a tunnel's requests (to the host's `127.0.0.1`) as coming from the
      server's own address, and its own requests to its address as coming
      from `169.254.1.3`. The provider's private network, which other
      customers share, isn't allowed. Roostack's own pages are a public
      route with a limit of 10 a minute on `/login` and
      `/backups/recovery-key`; `ui_tunnel_only: true` under `public` keeps
      them private.
    - The check link is `https://<address>/.well-known/roostack-check/<token>`.
      It's the only path on a public address that reaches Roostack, and it
      works for an hour.
