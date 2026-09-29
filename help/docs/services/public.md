# Make a service public

Services are for your home network (and VPN) unless you choose otherwise.
Making one **public** lets anyone on the internet reach it: handy for
sharing photos with family, or using your password manager away from home
without a VPN.

Only the Owner can make a service public. Roostack's own pages always stay
private.

## Before you do

- Anyone can reach the service's **login page**, and anything it shares
  without a login. Use strong passwords, and two-factor authentication if
  the service has it.
- Keep it up to date: a public service is found and tried by strangers
  within hours.

## Making it public

On the service's page, next to **Reachable from**, choose **Make
public…**. Roostack explains what changes, then:

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

!!! warning "Port 443 already used at home?"

    A router can forward port 443 to only one device. If another device at
    home already has it, Roostack can't share it. Public access through a
    cloud server, which needs no router changes, is planned.

## Parts that stay private

Some services have pages that shouldn't be public, such as an admin panel.
Their package marks them, and they stay reachable from home only, even when
the rest of the service is public. You can still make one public: tick it
when making the service public, after reading why it's marked.

A few services are marked as not safe to make public at all. Roostack says
why, and you have to confirm before it goes ahead.

## Making it private again

On the same page, choose **Make private**. The service is reachable from
home only again, and its DNS record points back home. Once nothing is
public, CrowdSec stops.

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
    - The check link is `https://<address>/.well-known/roostack-check/<token>`.
      It's the only path on a public address that reaches Roostack, and it
      works for an hour.
