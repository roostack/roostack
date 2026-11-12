# Server monitoring: mock-ups (2026-11-12)

How each server is doing: processors, memory, swap, load, network, and temperatures, with history and alerts, and Glances for a live, detailed view. The maintainer's direction (2026-11-10): a small core of Roostack's own, plus Glances for the depth, rather than re-implementing Glances or Beszel (Roostack Cloud's docs/SERVICES-TODO.md, "Server monitoring").

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/server-monitoring/monitoring.html>. The bar at the bottom right switches the page, and light or dark. Mock content: a lab of three servers (nuc, mum-and-dad, cloud-box) where mum-and-dad is low on memory.

## The pages

- **Monitoring** (`monitoring.html`): a **Servers** section above the services' checks: each server's processors, memory, swap, and temperature now, and whether it's fine, with **Details**. A server low on memory has a notice at the top, naming the service using the most.
- **A server** (`server.html`, `/monitoring/servers/<server>`): the figures now; **Over time** for a day, a week, or a month (processors, memory with swap, network in and out, temperature), each with its peak; **What's using it**, each service's memory and processor time over the last hour; and **Live view (Glances)…**.
- **Live view** (`glances.html`): Glances is off until an Owner or Admin turns it on for a server. Turning it on installs it from Debian and runs it there; **Open Glances** shows its own page through Roostack's.

## How it would work

- **The core, in Roostack (no new software):** each server reads `/proc` (processors, memory, swap, load, network), `/sys/class/hwmon` (temperatures), and each service's cgroup under the `roostack` user's systemd (memory and processor time) every minute, and reports it to the controller with its status. The controller keeps every reading for two days, then hourly averages and peaks for as long as History's setting, as Monitoring keeps its checks.
- **Alerts**, through the notifier like other problems (Problems group), on the Monitoring page and under Needs attention: less than 10% of memory free for 15 minutes; heavy swapping for 15 minutes; a temperature at its sensor's own high mark. Disk space already has its warnings (SPEC §6.7).
- **Glances:** Debian's `glances` package (so Debian's security updates keep it patched), installed by the agent when it's turned on and removed when it's turned off, and run as a sandboxed systemd service (`roostack-glances`) whose web server listens only on the server's address on Roostack's network. Roostack's pages pass it on after Roostack's own login, so it has no login of its own. It sees the host's processes and network but not inside containers, which would need Podman's socket; each service's figures come from the core instead.
- **Charts** are SVG drawn on the server, like the uptime bars: no charting library.

## Questions

1. **Where it lives:** on Monitoring (Admins and Owners can see it), with each server's page under Monitoring, rather than on Settings → Servers (Owners only)? *Proposal:* Monitoring, and the server's settings page links to it.
2. **The alerts' marks:** under 10% of memory free for 15 minutes, swapping heavily for 15 minutes, and a temperature at its sensor's high mark (or 90 °C where the sensor gives none)? Warnings or problems?
3. **Glances:** installed only when someone turns it on for a server, and removed when they turn it off (*proposal*), or always installed with Roostack (about 40 MB on every server, but nothing to wait for)?
4. **Who can turn Glances on:** Owners and Admins, as they can see Monitoring? It shows every process on the server, including their command lines.
5. **History:** every reading for two days, then hourly averages and peaks for History's setting (90 days by default), as the checks have?
