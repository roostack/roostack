# Servers

Your **lab** is everything Roostack runs for you. It can be one computer,
or several: one at home and one at a relative's, or a small server in the
cloud. Each computer is a **server** in the lab.

- One server **runs the lab**: it keeps the lab's settings and serves
  Roostack's pages for all of them. This is the first one you set up.
- The others **run services**. You manage them from the same pages.

Each server runs its own services, its own backups and its own updates.
The servers talk to each other over their own private, encrypted network,
so a server elsewhere is as easy to manage as one at home.

The Owner manages servers under **Servers**, which shows each
one's role, services, disk space, and whether it's up to date.

- [Add a server](add.md)
- [Move a service](move.md) to another server
- [When a server is gone](gone.md): bring its services back elsewhere
- [Storage for other labs](storage-for-others.md): space for a friend's
  backups, and yours at theirs

## Where each server is

Roostack asks where each server is (**At home** or **Somewhere else**),
because it can't tell for itself. It uses this for your backup plan: a
backup on a server somewhere else counts as away from home.

## Backups between servers: direct links

When a server backs up to another server's disk, Roostack links the two
directly, so the backups don't go through the server that runs the lab
(using its connection twice). It needs to know where one can reach the
other:

- **Both at home** (on the same network): their home-network addresses.
  Set both to **At home**.
- **One on the internet** (a cloud server): its own address.
- **Anything else** (a router forwarding a port, another network): enter
  the address under **Direct links** on the server's card, as host:port
  (WireGuard's UDP port, 51820 unless your router forwards another).

The backup plan says "linked directly", or warns that backups go through
the server that runs the lab, and what to set. A link that doesn't
connect within a few minutes is tried again the next day, or as soon as
you change the address; meanwhile the backups still work.

??? note "For technical users"

    A direct link is a WireGuard peer of its own on each of the two
    servers (marked `# roostack link` in `/etc/wireguard/roostack.conf`;
    the server that's dialled listens on UDP 51820, opened in its
    firewall). Without one, the controller forwards traffic between
    servers to the backup store port (TCP 8000) only, on Roostack's network
    only; its firewall drops everything else.

## Renaming or removing a server

Under **Rename or remove** on a server's card:

- **Rename** changes its name everywhere. Renaming the server that runs the
  lab restarts Roostack.
- **Remove** takes a server out of the lab: it leaves the lab's network and
  runs on its own again. Remove or move its services first.

??? note "For technical users"

    The server that runs the lab is the **controller**; the others are
    **nodes**. The controller keeps the desired state and sends each node
    its **assignment** (the services placed on it); the node applies it
    itself, and keeps running it if the controller is away.

    The lab's network is WireGuard (interface `roostack`, UDP port 51820,
    addresses in `100.96.0.0/24`). Over it, each server serves the lab API
    on port 8471 with mutual TLS: the controller runs the lab's own
    certificate authority (`/var/lib/roostack/labca`), and gives each
    server a certificate for a year, renewed automatically 120 days before
    it expires. A removed server's certificate stops working at once.
