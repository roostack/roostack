# Servers

Your **lab** is everything Roostack runs for you. It can be one computer,
or several: one at home and one at a relative's, or a small server in the
cloud. Each computer is a **server** in the lab.

- One server **runs the lab**: it keeps the lab's settings and serves
  Roostack's pages for all of them. This is the first one you set up.
- The others **run services**. You manage them from the same pages.

Each server runs its own services, its own backups, and its own updates.
The servers talk to each other over their own private, encrypted network,
so a server elsewhere is as easy to manage as one at home.

The Owner manages servers under **Servers**, which shows each
one's role, services, disk space, memory, CPU, and whether it's up to
date. The home page adds up the memory and CPU of every server: memory in
use is what the server can't free without swapping, and CPU is the share
of its processors' time they were busy over the last few seconds.

Memory also counts what programs hold in **swap**: memory the server has
moved out to its disk because it ran short. A program waits for that
memory when it next uses it, so a server with a lot in swap can be slow
at first, after a quiet spell, and fast after. When programs hold more
than the server has, the home page says so in a warning, and the Servers
page shows the part in swap as a striped stretch of each server's memory
bar. More memory, or fewer services on that server, is the cure.

Roostack keeps a share of each server's memory for itself and the
system, so its pages, the sign-in page, and the web addresses stay quick
whatever the services do: the services together may use all of the
server's memory but 400 MB. Past that, the server slows them down rather
than stopping them. When the services on a server would together need
more than that, Roostack also holds each one to about twice what its
package expects, and its page says when it's being slowed down. You can
choose another limit, or none, on its settings page, under
[Memory](../services/settings.md#memory).

??? note "For technical users"
    Roostack runs Caddy in `roostack-core.slice` (`MemoryLow=96M`), and
    the services in `roostack-services.slice` with `MemoryHigh` at the
    server's memory less 400 MB, each in its own
    `roostack-services-<name>.slice`, all in
    `/var/lib/roostack/home/.config/systemd/user/`. `roostack.service`
    and `roostack-agent.service` have `MemoryLow` (256 MB and 64 MB) and
    `OOMScoreAdjust=-500`. Limits are `MemoryHigh`, never `MemoryMax`:
    the kernel reclaims a service's memory and moves it to swap rather
    than killing it. Roostack's package also turns zswap on
    (`/usr/lib/tmpfiles.d/roostack-zswap.conf`), so memory moved to swap
    is first kept compressed in memory.

- [Add a server](add.md)
- [Move a service](move.md) to another server
- [When a server is gone](gone.md): bring its services back elsewhere
- [Storage for other labs](storage-for-others.md): space for a friend's
  backups, and yours at theirs

## Where each server is

Roostack asks where each server is (**At home** or **Somewhere else**)
because it can't tell for itself. It uses this for your backup plan: a
backup on a server somewhere else counts as away from home.

## How visitors reach a server's services

The services on another server can be reached in two ways, which you choose
on that server's card under **Servers**, in **Services**:

- **Served by the server itself:** visitors go straight to it, with the
  server's own certificates.
  - Its **public** services, when it has an address of its own on the
    internet, such as a cloud server: a busy service there won't send all
    its visitors through your home's internet connection and back. It also
    runs its own protection (CrowdSec), and Roostack will point their DNS
    records at it. This is Roostack's choice for a cloud server.
  - Its **public** services when it has no address of its own on the
    internet, if your lab's way in is a cloud server
    ([Through a cloud server](../services/public.md#through-a-cloud-server-without-touching-your-router)):
    that cloud server will pass their visitors straight to it rather than
    through the server that runs the lab. It needs to reach the cloud
    server's WireGuard port, UDP 51820, which you may have to allow in that
    server's firewall at its provider; until it can, the card says so and
    its public services go through the server that runs the lab.
  - Its **private** services, when it's on your home network, the same one
    as the server that runs the lab: your devices at home reach it
    directly. Set **Where** to **At home** on its card for this.
- **Through the server that runs the lab:** visitors come to the server
  that runs the lab, which passes them on. This is Roostack's choice for a
  server at home.

Either way, the server that runs the lab still passes on all of the
server's services: for the lab's VPN devices, and for devices that look
your lab's names up with your lab's own DNS server, Pi-hole, or AdGuard Home,
which point them at it. That's also how your devices at home reach the
parts of a public service on a cloud server that stay private
([Parts that stay private](../services/public.md#parts-that-stay-private)).
The card says which of the server's services it serves itself, and why not
the others.

??? note "For technical users"

    Serving directly, the server runs Roostack's Caddy (`roostack-caddy`)
    and CrowdSec for its own names alone. Its public names get Let's
    Encrypt certificates from the HTTP or TLS challenge, so it holds no DNS
    login. Each private name served at home gets its own site, with a DNS
    challenge through Caddy's `acmedns` module, pointed at the server's own
    Roostack on `127.0.0.1:8483`: that passes the update to the server that
    runs the lab over the lab API (`/v1/challenge`), which sets
    `_acme-challenge.<name>` through your DNS provider, for that server's
    own names only, and removes it after ten minutes. A walled service's
    checks go to the lab's sign-in page the same way (`/v1/wall`). Its
    routes are published on its loopback and on its address on the lab's
    network. The choice is `public.serve.<server ID>: direct | controller`
    in `lab.yaml`.

## Backups between servers: direct links

When a server backs up to another server's disk, Roostack links the two
directly so that the backups don't go through the server that runs the lab
(using its connection twice). It needs to know where one can reach the
other:

- **Both at home** (on the same network): their home-network addresses.
  Set both to **At home**.
- **One on the internet** (a cloud server): its own address.
- **Anything else** (a router forwarding a port, another network): enter
  the address under **Direct links** on the server's card, as host:port
  (WireGuard's UDP port, 51820 unless your router forwards another).

The backup plan says "linked directly", or warns that backups are going
through the server that runs the lab, and what to set. A link that doesn't
connect within a few minutes will be tried again the next day or as soon as
you change the address; meanwhile the backups will still work.

??? note "For technical users"

    A direct link is a WireGuard peer of its own on each of the two
    servers (marked `# roostack link` in `/etc/wireguard/roostack.conf`;
    the server that's dialled listens on UDP 51820, opened in its
    firewall). Without one, the controller forwards traffic between
    servers to the backup store port (TCP 8000) only, on Roostack's network
    only; its firewall drops everything else.

## Cleaning up a server

Each server's card has **Clean up**, for what takes space there that no
service needs:

- **Old versions of services:** each update downloads the service's new
  version and keeps the one before so that Roostack can go back if the
  new one doesn't work. Earlier ones, and those of removed services, are
  listed with their sizes; tick the ones to delete. The version before
  each service's last update will be kept for a week and isn't listed.
- **Data from removed services** and **kept copies**, which have their
  own lists (Services → Removed, data kept, and the server's card).
- **Roostack's own:** earlier versions of its web proxy's image, which nothing uses any more.

**By itself**, which is on unless you turn it off, Roostack will delete
old versions of services a week after nothing uses them, every night
after the backup, and History will say what it deleted.

??? note "For technical users"

    Images are listed with `podman images` and deleted with `podman image
    rm`, as the `roostack` user. An image is in use while a container uses
    it, a Quadlet file in `~roostack/.config/containers/systemd/` names
    it, or an installed service's package names it (a turned-off service
    keeps its images). When each image was last named is kept in the
    server's settings (`images.seen`).

## Renaming or removing a server

Under **Rename or remove** on a server's card:

- **Rename** will change its name everywhere. Renaming the server that runs
  the lab will restart Roostack.
- **Remove** will take a server out of the lab: it will leave the lab's
  network and run on its own again. Remove or move its services first.

??? note "For technical users"

    The server that runs the lab is the **controller**; the others are
    **nodes**. The controller keeps the desired state and sends each node
    its **assignment** (the services placed on it); the node applies it
    itself and keeps running it if the controller is away.

    The lab's network is WireGuard (interface `roostack`, UDP port 51820,
    addresses in `100.96.0.0/24`). Your servers aren't your home network:
    over it, a server reaches only what Roostack opens for servers (the
    lab API, services' web ports for the lab's Caddy, backup stores, an
    edge's way in to Caddy, and the sign-in page while an app on it uses
    single sign-on), and SSH only from the server that runs the lab. On the
    server that runs the lab, only Caddy, of what runs as the `roostack`
    user, reaches the other servers' web ports, live views and SSH: its
    services can't go round it. It never reaches Roostack's pages, private services, a service's
    home-only extra connections, or SSH from another server, so a cheap
    cloud server in the lab can't get at home what a laptop there could. Over it, each server serves the lab API
    on port 8471 with mutual TLS: the controller runs the lab's own
    certificate authority (`/var/lib/roostack/labca`), and gives each
    server a certificate for a year, renewed automatically 120 days before
    it expires. A removed server's certificate stops working at once.
