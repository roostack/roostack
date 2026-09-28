# Add a server

You can add another computer to your lab, at home or elsewhere, to run
some of your services or keep backups.

## 1. Install Roostack on it

[Install Roostack](../start/install.md) on the new computer, as you did on
the first. Don't go through its setup page: joining the lab sets it up.

## 2. Make a join code

On your lab's Roostack, go to **Settings → Servers**, and under **Add a
server**:

1. Enter the **address the new server reaches this one at**:
    - for a server **at home**: this server's address on your home network,
      and port 51820, such as `192.168.1.20:51820`;
    - for a server **elsewhere**: your home's public address and port 51820,
      such as `203.0.113.7:51820`, once you've set up port forwarding (below).
2. Choose **Make a join code**.

Roostack shows a command with the code in it. The code works once, for an
hour. Anyone with it can add a server to your lab, so don't share it.

## 3. Join

On the new server, run the command Roostack showed you:

```bash
sudo roostack node join <code>
```

It connects to your lab, and shows up in **Settings → Servers** as up to
date. It's ready to run services: choose it under **Run it on** when you
[add a service](../services/add.md), or [move one](move.md) there.

## A server outside your home: port forwarding

A server elsewhere connects to your lab through your home router, which
only lets it in if you ask it to. Roostack has already opened its own
firewall; the router is the step it can't do for you.

1. Open your router's settings page. It's often `http://192.168.1.1` or
   `http://192.168.0.1`; the address and password are usually on a
   sticker on the router.
2. Find **Port forwarding**. Some routers call it *Virtual server*, *NAT*
   or *Port mapping*, or put it under *Advanced* or *Firewall*.
3. Add a rule: protocol **UDP** (not TCP), port **51820**, to the server
   that runs your lab, port 51820.
4. Use your home's public address in the join code. Search "what is my IP"
   from a device at home to find it. If it changes now and then, a name
   from your domain that points home works better.

If joining says it **can't reach the controller**, the port isn't forwarded
yet, or the address is wrong.

!!! warning "Some connections can't forward ports"

    Some internet connections (called *CGNAT*, common on mobile and some
    fibre connections) can't forward ports at all. Then make the server
    that's reachable (in the cloud, say) the one that runs the lab.

??? note "For technical users"

    The join code carries the controller's endpoint, WireGuard public key
    and CA fingerprint, the new server's address on the lab's network, a
    one-time WireGuard key and a token. `node join` brings WireGuard up
    with the one-time key, sends a certificate request to the controller's
    lab API (`100.96.0.1:8471`), checks the controller's certificate against
    the fingerprint, gets its own certificate, then switches to its own
    WireGuard key.
    It records its role in `/var/lib/roostack/labnet/membership.json`.
