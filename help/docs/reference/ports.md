# Network ports

Roostack's firewall (nftables) blocks everything that isn't listed here.

## Open to your network, or the internet

| Port | For | Open to |
|---|---|---|
| 80/tcp | Web (redirects to HTTPS) | Anyone who can reach the server |
| 443/tcp, 443/udp | Your services and Roostack, over HTTPS (udp: HTTP/3) | Anyone who can reach the server |
| 22/tcp | SSH | Your choice: home network only, anyone, or nobody. Over the lab's network, only from the server that runs the lab |
| 51820/udp | The lab's network (WireGuard), on the server that runs the lab | Anyone, so servers elsewhere can join |
| 51821/udp | Other labs storing backups here (WireGuard), on the server offering the space | Anyone, so other labs can connect |
| Your choice | A service's [extra connections](../services/settings.md#extra-connections), such as git over SSH | Your home network |
| 53/udp, 53/tcp | DNS, while a service such as Pi-hole uses it: the firewall sends it to the service's own port (5053) | Your home network, and the VPN devices you let reach the service; never the internet |
| 8443/tcp | The [setup page](../start/install.md#when-its-installed), over HTTPS, only until first-run setup is finished | Your home network |

Services are private, so while "anyone" can reach port 443, a service
answers only visitors from your home network (or a VPN into it). Nothing is
reachable from the internet unless your router forwards a port to the
server.

## Only over Roostack's own networks

Only your lab's servers reach these, over the lab's network; other labs
reach only their own storage here. Your servers aren't your home network:
over the lab's network they reach nothing else.

| Port | For |
|---|---|
| 8471/tcp | The lab API between your servers (mutual TLS), on the lab's WireGuard network |
| 8000–8001/tcp | A server's backup store for your other servers (rest-server), on the lab's network |
| 8472/tcp, 8100 and up | Other labs' access to their backups here: the invite and usage API, then two ports for each lab (its store and its admin access) |
| Services' local ports | On a server other than the one running the lab, its services' web ports, for the lab's Caddy, on the lab's network |
| 61208/tcp | A server's [live view](../servers/monitoring.md#a-live-view-with-glances) (Glances), while it's on, on a server other than the one running the lab, and only that one can reach it |

## Only on the server itself

Anyone with a login on the server itself can reach these ports, and a
service's web port lets them in without the sign-in page. So Roostack
assumes that everyone with a login on the server is someone you trust as
you would an Owner. Don't give other people a login on it.

| Port | For |
|---|---|
| 127.0.0.1:8480 | Roostack's web pages (Caddy passes them on as `roost.<your domain>`) |
| 127.0.0.1:61208 | The [live view](../servers/monitoring.md#a-live-view-with-glances) (Glances) of the server running the lab, while it's on (Caddy passes it on as `glances-<server>.<your domain>`) |
| 127.0.0.1:8473 | CrowdSec's local API, while anything is [public](../services/public.md), for Caddy's bouncer |
| 127.0.0.1:20000–29999 | Each service's web port, for Caddy; chosen once per service and kept |

??? note "For technical users"

    The firewall is the table `inet roostack`, generated from the lab and
    its services, applied by `roostack-agent` with `nft -f` (checked with
    `nft -c` first), and saved to `/etc/roostack/firewall.nft`. See it
    with:

    ```bash
    sudo nft list table inet roostack
    ```

    "Your home network" means these ranges, for both the firewall and
    Caddy's private check: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
    `100.64.0.0/10` (also used by VPNs such as Tailscale), `fc00::/7`, and
    `fe80::/10`, **except** your lab's servers on its network
    (`100.96.0.0/17`), other labs storing backups here (`100.100.0.0/14`),
    and Roostack Cloud's relay (`100.81.0.0/16`). `100.96.128.0/17` is kept
    for your own devices joining the lab's network, still to be built, and
    counts as home. The firewall settles Roostack's own interfaces first:
    on `roostack`, a server reaches only the ports in the second table and
    SSH from the server that runs the lab.
    `roostack apply -dry-run` prints the firewall Roostack would apply.

    **Traffic passing through a server** (its `forward` chain): Roostack
    rules only on what touches its own networks. Nothing passes to or from
    other labs or Roostack Cloud's relay; on `roostack`, only backups from
    one server to another's store pass through the server they're sent
    through; the [VPN's devices](../settings/vpn.md#what-a-device-reaches)
    pass only where they're allowed; and where Roostack turned forwarding
    on for the VPN, only answers to those devices pass, and connections
    other software sends on, such as to a container's published port.
    Once anything turns forwarding on for the whole server
    (`net.ipv4.ip_forward`), Roostack stops limiting those interfaces and
    won't turn their forwarding off again. Everything else
    passing through is left alone: if other software on the server, such
    as Docker or a host for virtual machines, routes traffic between its
    own networks, Roostack's firewall lets it through so that it keeps
    working, and that software's own rules protect it. Roostack never turns
    forwarding on for it.

    Caddy runs in its own network namespace (pasta), so it sees visitors'
    real addresses; it reaches the services' local ports through pasta's
    loopback mapping.
