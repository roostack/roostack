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
public…**. Roostack explains what changes and asks you to confirm, then:

1. **Protection.** Roostack starts **CrowdSec**, which spots attacks in
   the web log and blocks the addresses they come from, and it limits how
   fast anyone can try passwords. The first time, you choose whether
   CrowdSec shares attack reports with its community (only attackers'
   addresses and what they tried). Sharing gets you the community's list
   of known attackers in return. You can change this in **Settings →
   Public access**.
2. **DNS record.** Once CrowdSec is running, Roostack points the service's
   address at your home's public address. If Roostack doesn't manage your
   DNS records, the page shows the record to add yourself.
3. **Your router.** Forward TCP ports 80 and 443, and UDP port 443, to this
   server's home address (the page shows it). Look for "port forwarding",
   "virtual server" or "NAT" in your router's settings. If the server is a
   cloud server rather than at home, skip this.
4. **Check from your phone.** Choose **Make a check link**, turn Wi-Fi off
   so your phone uses mobile data, and scan the code. The step ticks itself
   when your phone gets through.

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

## Parts that stay private

Some services have pages that shouldn't be public, such as an admin panel.
Their package marks them, and they stay private (reachable from home, or on
a cloud server only through an SSH tunnel) even when the rest of the
service is public. You can still make one public: tick it when making the
service public, after reading why it's marked, or later under **Parts kept
private** on the same page. The service's page says when one of its links
leads to a private part.

A few services are marked as not safe to make public at all. Roostack says
why, and you have to confirm before it goes ahead.

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
- **DNS:** one record, `*.<your domain>`, points at the server, and covers
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

## Making it private again

On the same page, choose **Make private**. The service is reachable from
home only again (on a cloud server, through an SSH tunnel only), and at
home its DNS record points back home. Once nothing is public, and
Roostack's own pages aren't either, CrowdSec stops.

??? note "For technical users"

    - Making an address public sets `exposure: public` on its route in the
      instance file (`/var/lib/roostack/services/<name>.yaml`); private
      paths you opened are listed under `public_paths`.
    - Caddy's handler for a public address first refuses outside visitors
      on private paths, then asks CrowdSec's bouncer, then applies a rate
      limit per visitor address: 1000 requests per 10 seconds, and 10 a
      minute on the login pages the package names.
    - CrowdSec runs as `roostack-crowdsec` while anything is public, reading
      Caddy's JSON access log from `/var/lib/roostack/caddy-logs`. Its
      configuration and database are in `/var/lib/roostack/crowdsec`, and
      its local API is on `127.0.0.1:8473`. `cscli decisions list`, run in
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
