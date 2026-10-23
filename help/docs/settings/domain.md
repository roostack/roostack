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
    `smiths.roost.ac`. It uses Roostack Cloud, with a free account: see
    [Roostack Cloud](cloud.md) for how the lab is linked to it.

Your own domain
:   A domain you own, such as `smith.family`: services at
    `photos.smith.family`, and Roostack at `roost.smith.family`.

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

