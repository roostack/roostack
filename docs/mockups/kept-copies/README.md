# Kept copies: mock-ups (2026-10-23)

For the quality review's D12, before building: one kind of kept copy in place of two (a restore's `<folder>.before-restore-…` and a move's or an import's `<service>.before-move-…` and the rest), shown as one "Kept copies" list, each copy with a label saying why it's there.

- **restore.html:** a service's restore page lists its kept copies on every server, as now.
- **servers.html:** the Servers page lists each server's kept copies, of every service, with the same labels and buttons (today it has a shorter list, with Delete only).

What's new in both:
- one item per copy (all the folders set aside at one moment), not one per folder;
- a label for why it's there, its time and size, and its folders under "Where it is";
- **Put back…** wherever it can go back (on the server the service runs on, and not a partial copy), and a sentence where it can't.

**Decided (the maintainer, 2026-10-23):** build it as shown, with a restore from another server labelled "From before a restore", and **Put back…** on the Servers page too. Built in SPEC §8.4's "Kept copies — one list".

The labels: From before a restore; From a restore that didn't finish; From before a move; From a move that didn't finish; From before bringing data in; From bringing data in that didn't finish; From before a put back.

They use the real `tokens.css`, themes and `app.css`; `mock.css` holds only what the real stylesheet lacks. **Open them** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8797 --bind 127.0.0.1
```

then <http://127.0.0.1:8797/docs/mockups/kept-copies/restore.html>. The bar at the bottom right switches the page, and light or dark.
