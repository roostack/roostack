# Add a server

You can add another computer to your lab, at home or elsewhere, to run
some of your services or keep backups.

## 1. Install Roostack on it

[Install Roostack](../start/install.md) on the new computer, as you did on
the first. Keep its setup code: you'll need it to join.

## 2. Make a join code

On your lab's Roostack, go to **Servers**, and under **Add a
server** choose **Make a join code…**:

1. Choose the **address the new server reaches this one at**. Roostack will
   list the ones it knows and suggest the one you used last time:
    - for a server **at home**: **this server's home-network address**, such
      as `192.168.1.20:51820`;
    - for a server **elsewhere**: **a name from your domain**,
      `link.<your domain>:51820`, once you've set up port forwarding (below).
      Roostack will keep that name pointing at your home's public address,
      which can change. (It will only create the name the first time you
      choose it.) **The public address** works too, until it changes;
    - **another address**, for anything else: a VPN address, a router that
      forwards a different outside port, or a name from another dynamic DNS
      service;
    - for a **cloud server** joining a lab at home: **None: this server
      connects to the new one instead**, and the cloud server's public
      address. Then nothing will change on your router (below).
2. Choose **Make a join code**.

Roostack will show the join code, which works once, for an hour. Anyone
with it can add a server to your lab, so don't share it.

## 3. Join

Open the new server's setup page (the address its installer showed), enter
its setup code, choose **Add it to a lab you already have**, paste the join
code, and give the server a name. Choose **Join the lab**: it will connect
and restart as part of your lab, and the page will say where to manage it.
If it can't connect, it will say why, and nothing will be changed: fix it
and try again.

On the command line instead, under the code there's **Or on the new
server's command line**:

```bash
sudo roostack node join <code>
```

Either way, it will show up in **Servers** as up to date. It's ready to run
services: choose it under **Run it on** when you
[add a service](../services/add.md), or [move one](move.md) there.

It will take the lab's time zone (the one of the server that runs the lab),
so backups, updates, and logs line up across your servers. To give it a
different one, change it on its **Time zone** page.

## A cloud server: your lab connects to it

A cloud server (a VPS) has a public address, so your lab can connect out
to it, and your router needs no change. Choose **None: this server
connects to the new one instead** and enter the cloud server's public
address (its provider shows it, such as `203.0.113.7`).

The cloud server has to let your lab in on **UDP port 51820**. Roostack
will open it in the server's own firewall while it joins and keep it open.
If its provider has a firewall of its own (Linode's *Cloud Firewall*,
for example), add a rule there: inbound, **UDP**, port **51820**.

If joining says your lab **didn't connect to this server**, that port is
closed at the provider, or the join code was made for another address.

## A server outside your home: port forwarding

A server elsewhere can also connect to your lab through your home router,
which only lets it in if you ask it to. Roostack has already opened its own
firewall; the router is the step it can't do for you.

1. Open your router's settings page. It's often `http://192.168.1.1` or
   `http://192.168.0.1`; the address and password are usually on a
   sticker on the router.
2. Find **Port forwarding**. Some routers call it *Virtual server*, *NAT*,
   or *Port mapping*, or put it under *Advanced* or *Firewall*.
3. Add a rule: protocol **UDP** (not TCP), port **51820**, to the server
   that runs your lab, port 51820.
4. Choose **a name from your domain** for the join code.

If joining says it **can't reach the controller**, the port isn't forwarded
yet, or the address is wrong.

!!! warning "Some connections can't forward ports"

    Some internet connections (called *CGNAT*, common on mobile and some
    fibre connections) can't forward ports at all. A cloud server doesn't
    need it (above). Two servers both behind routers like that can't join
    each other yet.

??? note "For technical users"

    The join code carries the controller's endpoint, WireGuard public key
    and CA fingerprint, the new server's address on the lab's network, a
    one-time WireGuard key, and a token. `node join` brings WireGuard up
    with the one-time key, sends a certificate request to the controller's
    lab API (`100.96.0.1:8471`), checks the controller's certificate against
    the fingerprint, gets its own certificate, then switches to its own
    WireGuard key.
    It records its role in `/var/lib/roostack/labnet/membership.json`.
    With **None: this server connects to the new one instead**, the code
    has no endpoint and says *listen*: the new server's WireGuard listens on
    UDP 51820 (opened in Roostack's firewall for the join, and kept open by
    its assignment afterwards), and the controller dials it at the address
    typed, with a 25-second keepalive that keeps the home router's NAT
    mapping open. The controller keeps dialling it there once it has
    joined (`servers.dial` in its database).
