# Your domain

A **domain** is a name you own on the internet, such as `smith.family`.
Roostack gives each service its own address under it, such as
`passwords.smith.family` and `photos.smith.family`, and Roostack itself is
at `roostack.smith.family`.

You buy a domain from a company such as Cloudflare or Porkbun, for a few
pounds a year. The company that runs its **DNS** (the internet's address
book) is your **DNS provider**; it's usually the same company.

## Secure addresses (HTTPS)

Every address gets a secure certificate, so your browser shows the padlock
and nobody on your network can read what's sent. Roostack gets one
certificate for your whole domain, and renews it by itself.

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

The domain page shows what each one needs, and where to get it.

!!! warning "Namecheap"

    Namecheap's system rewrites every record on your domain to change one.
    Roostack can use it, but asks you to accept the risk first. Moving your
    DNS to Cloudflare (free) avoids it.

## Where your lab is

**Settings → Where it is and DNS** says whether your lab is **at home**,
behind a router, or **on the internet**: a cloud server (a VPS) with a
public address of its own. The setup wizard asks the same question, and
suggests an answer from the server's addresses.

At home
:   Services are for your home network (or a VPN into it). Making one
    public means forwarding ports on your router. Roostack's own pages stay
    private.

On the internet
:   There's no home network, so services start public, protected by
    CrowdSec, and private ones are only reachable through an SSH tunnel
    ([how](../services/public.md#private-on-a-cloud-server)). Roostack's own
    pages are at `roostack.<your domain>`, behind your password and a code,
    unless you keep them to an SSH tunnel. One DNS record, `*.<your
    domain>`, points at the server's public address.

Changing it restarts Roostack.

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
    AdGuard or a router setting. Point it at your server there, or remove
    the override.

The record isn't visible yet
:   DNS changes can take a few minutes. Roostack checks again by itself.

**Give your server a fixed address** in your router (a *DHCP reservation*),
so its address at home doesn't change.

If a record with that name already exists (one you made before), Roostack
leaves it alone, and offers **Take over this record…**.

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
    home network's resolver and through `1.1.1.1`, to tell a missing record
    from a router blocking it. Checks run every 30 minutes.
