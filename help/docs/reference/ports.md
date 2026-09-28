# Network ports

Roostack's firewall (nftables) blocks everything that isn't listed here.

## Open to your network, or the internet

| Port | For | Open to |
|---|---|---|
| 80/tcp | Web (redirects to HTTPS) | Anyone who can reach the server |
| 443/tcp, 443/udp | Your services and Roostack, over HTTPS (udp: HTTP/3) | Anyone who can reach the server |
| 22/tcp | SSH | Your choice: home network only, anyone, or nobody |
| 51820/udp | The lab's network (WireGuard), on the server that runs the lab | Anyone, so servers elsewhere can join |
| 51821/udp | Other labs storing backups here (WireGuard), on the server offering the space | Anyone, so other labs can connect |
| Your choice | A service's [extra connections](../services/settings.md#extra-connections), such as git over SSH | Your home network |

Services are private, so while "anyone" can reach port 443, a service
answers only visitors from your home network (or a VPN into it). Nothing is
reachable from the internet unless your router forwards a port to the
server.

## Only over Roostack's own networks

| Port | For |
|---|---|
| 8471/tcp | The lab API between your servers (mutual TLS), on the lab's WireGuard network |
| 8000–8001/tcp | A server's backup store for your other servers (rest-server), on the lab's network |
| 8472/tcp, 8100 and up | Other labs' access to their backups here: the invite and usage API, then two ports for each lab (its store and its admin access) |
| Services' local ports | On a server other than the one running the lab, its services' web ports, for the lab's Caddy, on the lab's network |

## Only on the server itself

| Port | For |
|---|---|
| 127.0.0.1:8480 | Roostack's web pages (Caddy passes them on as `roostack.<your domain>`) |
| 127.0.0.1:20000–29999 | Each service's web port, for Caddy; chosen once per service and kept |

??? note "For technical users"

    The firewall is the table `inet roostack`, generated from the lab and
    its services, applied by `roostack-agent` with `nft -f` (checked with
    `nft -c` first) and saved to `/etc/roostack/firewall.nft`. See it with:

    ```bash
    sudo nft list table inet roostack
    ```

    "Your home network" means these ranges, for both the firewall and
    Caddy's private check: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`,
    `100.64.0.0/10` (also used by VPNs such as Tailscale), `fc00::/7` and
    `fe80::/10`.
    `roostack apply -dry-run` prints the firewall Roostack would apply.

    Caddy runs in its own network namespace (pasta), so it sees visitors'
    real addresses; it reaches the services' local ports through pasta's
    loopback mapping.
