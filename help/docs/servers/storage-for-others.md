# Storage for other labs

Family and friends who also run Roostack can keep a copy of each other's
backups. Each of you then has a backup away from home, without paying for
online storage.

Their backups are **encrypted before they leave their lab**, so you can't
read them, and they can't read yours. Each lab gets its own space, with a
size limit you choose. Within it, each of their servers can only add to
its own backups: one that's taken over can't touch the others'.

## Offering space to someone

On **Settings → Storage for others**, under **Invite a lab**:

1. Enter **whose lab** it is, and **how much space** they get (in GB).
2. Choose the **address their lab reaches your server at**: best, **a name
   from your domain** (`link.<your domain>:51821`), which Roostack keeps
   pointing at your home's public address; or **the public address**, or
   **another address**.
3. Choose **Make an invite**.
4. Send them the invite privately. It works once, for 24 hours.

Your router must let them in: add a port forwarding rule for **UDP** port
**51821** to the server that keeps their backups (see the steps in
[Add a server](add.md#a-server-outside-your-home-port-forwarding), with
51821 instead of 51820).

If you have several servers, **Offered by** chooses which one's disk keeps
their backups.

The page lists each lab storing with you: how much it uses, when it last
connected, and a way to change its space.

## Storing your backups with someone

Ask them for an invite. Then, on your **Backups** page, add a backup, choose
**A friend's lab**, and paste the code into **Their invite code**. See
[Where backups can go](../backups/places.md).

## Stopping

Choose **Remove** next to a lab. They can't add backups from then on.
Roostack tells their lab, and deletes their backups here after **30 days**,
in case it was a mistake, or sooner if you choose **Delete now**.

??? note "For technical users"

    Other labs connect over a second WireGuard interface,
    `roostack-guests` (UDP 51821), separate from your own lab's network.
    Each guest lab gets its own pair of rest-server stores (append-only
    for its servers, and an admin one for its controller's clean-ups),
    with a quota, on the offering server, under
    `/srv/roostack/backups/guests/g<number>`. Each of its servers has its
    own user there (`--private-repos`), added by their controller over
    the guest API when that server's backups are first set up, and removed
    when the server leaves their lab. Your lab keeps only the passwords'
    hashes. On their side, the link to your lab is a WireGuard interface
    named `roostack-st1` (up to `roostack-st9`), and their servers'
    backups are forwarded through their controller.
