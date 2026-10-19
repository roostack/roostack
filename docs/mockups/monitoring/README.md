# Monitoring: mock-ups (2026-10-19)

For SPEC §6.15, before building: a service's uptime card, the Monitoring page (a main item beside Servers; every check, your own checks, the public status page's settings, the switch), a check's Details, and the public status page at `status.<lab>`.

They're made from pages of a running lab (VM3), with the real `tokens.css`, themes and `app.css`, and mock content: a lab of three servers (nuc, mum-and-dad, cloud-box) where Immich has just stopped answering, Jellyfin is updating (paused) and Mealie's certificate isn't renewing. `mock.css` holds only what the real stylesheet lacks (the uptime bar, the answer-time line, the public page's layout).

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/monitoring/service.html>. The bar at the bottom right switches the page, and light or dark.
