# Install and a service's settings: mock-ups (2026-10-20)

For the forms review (docs/DESIGN.md "Forms", TASKS.md "Forms"), before building: the install form (`install.html`) and a service's settings (`service-settings.html`), the two long forms the Owner wanted mocked first.

They're made from VM3's pages (the real `tokens.css`, themes and `app.css`; Forgejo's and Plex's packages from the test registry, their real credits and "What it sets up"), with the controls built on 2026-10-19 and 20 (segmented control, fields side by side, switches). `mock.css` holds only what the real stylesheet lacks: a form's section headings, a Details disclosure inside a form, the summary beside the form (install B).

**Open them** through a web server at the repo root:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/forms/install-forgejo-a.html>; the bar at the bottom right switches pages, and light or dark.

**Chosen** (the Owner, 2026-10-20): **B** for the install form, **A** for a service's settings; built (docs/DESIGN.md "Forms").

## Install

Today: Title, Name, Run it on, the media libraries, Web address, then the package's settings, one under another, in a narrow card (460 px).

- **A, one card, in sections** (recommended): a wider card (680 px). **Name and address**: Title and Web address side by side, the Name (`forgejo-…`, rarely changed and never again) behind a Details line that shows it ("Its name for folders and backups: `forgejo`"); Run it on. **Where it runs** (when it reads media libraries): the server and its libraries, ticked side by side. **<App>'s settings**: the package's text settings two to a row (Forgejo's administrator username and email, then its password), its on/off settings as switches. Then what it sets up, and Install.
- **B, the summary beside it**: A's form, with "What <app> sets up" in a card of its own beside the form, staying in view as you scroll (under it on phones). Uses a wide screen; but the summary changes as you fill in the form (its address, its server's memory), and beside it that's easy to miss, while under the form it's read just before Install.

Building A needs no change to packages: text settings go two to a row by the order the package lists them. A setting's type stays what the package says (Forgejo's email is a plain text field until packages can say `email`).

## A service's settings

Today: three cards, each with its own Save: Settings (web address and the package's settings), Updates (four radios with notes), Name (Rename); and, for a media app, a Media libraries card with its own Save too.

- **A, one form** (recommended): the web address, the package's settings (switches), the media libraries it reads, and **Updates** as a segmented control (Tell me, Fixes, Fixes and features, Everything) with the chosen one's note under it and the rules for waiting under Details; one Save, restarting the service only when what it runs changes (its address, settings or libraries). Name stays a card of its own: renaming is a different act, and saves at once.
- **B, separate cards, restyled**: the same controls, each card still with its own Save. Nothing to learn about what one Save does; but four Saves on a page, and a change in one card is lost when another is saved.
