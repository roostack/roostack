# The big red button: mock-ups (2026-10-22)

For the proposal in SPEC §6.16 and [docs/RED-BUTTON-PLAN.md](../../RED-BUTTON-PLAN.md), before anything is built. They use the word the plan recommends, **Pause public access** (question 3).

The story: Smiths' lab runs Ghost, Immich and Lychee publicly and Vaultwarden at home. The service registry has published a warning about PHP that's being used in attacks. Lychee is built with PHP, and the lab is set to pause in that case, so Roostack has paused Lychee's public access by itself.

- **Home** (`home.html`): what's paused and why, a warning that didn't pause anything (rated high), and **Pause public access…**, red-outlined beside Add a service.
- **Pause** (`pause.html`): what to pause (everything public, by what it's built with, or chosen services), what will happen, and **Pause now**.
- **Paused** (`paused.html`): Home after pausing everything, with the confirmation for opening it again. Lychee will stay paused because the warning still applies.
- **A service** (`service.html`): Lychee's page, with the warning, what to do (the update) and **Keep it public anyway…**.
- **Settings** (`settings.html`): Settings → Public access: what's paused now, what Roostack will do when the registry warns of a problem, and what the lab is built with.
- **A visitor** (`visitor.html`): what visitors from the internet see. It's a real error page from `internal/errpage` (the "asleep" scene) with the proposed words.

The app pages use the monitoring mock-ups' shell (a page saved from VM3) with the real `tokens.css`, themes and `app.css`. `mock.css` holds only what the real stylesheet lacks: the red-outlined button, the lists on the pause page, the warning card, and the confirmation shown open in the page. The pause icon is drawn in the pages; `icons.go` would gain it. The logos are the registry's.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

Then go to <http://127.0.0.1:8791/docs/mockups/red-button/home.html>. The bar at the bottom right switches between the pages, and between light and dark.
