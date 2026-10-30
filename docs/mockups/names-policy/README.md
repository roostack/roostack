# roost.ac names policy: mock-up (2026-10-30)

What Owners see when Roostack Cloud doesn't allow a service's public access through roost.ac (SPEC §6.11, "Roostack Cloud's names and relay"), before it was built. The maintainer approved it on 2026-10-30 with two changes, both made here: no "carry" (Roostack Cloud "only allows public access … for approved accounts"), and section 6's table no longer overflowing its card.

The story: Smiths' lab is on `smiths.roost.ac`, and its account isn't approved. It runs Zipline, which the registry flags, and Notes, from one of the Owner's own packages. Sentences in italics are Roostack Cloud's own, as its answer sends them.

Open it through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

Then go to <http://127.0.0.1:8791/docs/mockups/names-policy/index.html>.
