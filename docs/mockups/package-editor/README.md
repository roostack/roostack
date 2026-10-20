# The package editor: mock-up (2026-10-20)

**Chosen (2026-10-20)**, with the Owner's wording (below); built the same day (SPEC.md §8.3.7, "The package editor").

**What the Owner asked for (2026-10-20):** an editor for Roostack's package format that runs entirely in the browser, imports a Docker Compose file, gives help and suggestions for every field, and flags what the registry's package policy would refuse. It runs in three places: on roostack.cloud, downloaded and opened from a file with no network, and inside Roostack (from **Add your own**).

**Decided already (the Owner, 2026-10-20):**

- **Staying in step with `roostack validate`:** Go generates what the editor knows (fields, fixed lists, patterns, limits, the Quadlet allow-list and refused lines). The checks that are code are written once in JavaScript, and one set of test cases, run by Go and by `node --test` in CI, fails when they disagree. Considered: Go's validator compiled to WebAssembly (several MB, and `'wasm-unsafe-eval'` in the website's CSP).
- **YAML:** the `yaml` library (2.9.1, ISC), bundled once into a single script and vendored. It keeps comments and edits a file in place. Considered: js-yaml (drops comments).
- **Inside Roostack:** **Check with this Roostack** (the server's real checks, templates rendered, nothing added) and **Add to this lab** (through `/catalog/add`'s checks, as today).
- **The website** gets its page now, in roostack-cloud.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8792 --bind 127.0.0.1
```

then <http://127.0.0.1:8792/docs/mockups/package-editor/>. The bar at the bottom right switches the view (Start, Import, Edit), where the editor runs (Roostack, the website, a downloaded file), which field's help is open, dark mode and a phone-width frame. Choices go into the address (`?view=edit&where=website&help=login&tab=yaml`).

It's the real `tokens.css`, Teal theme and `app.css`; `mock.css` holds only what they lack, and `mock.js` is plain script (the real editor can't use Alpine: the website's CSP has no `'unsafe-eval'`). The example is an import of a Vikunja Compose file (an app with Postgres, not in the registry), made up for the mock-up.

## The views

**Start.** Three ways in: import from Docker Compose, open a package file, start from scratch; then the registry's three examples (Vaultwarden, Forgejo, Immich), as **Write a package** describes them. Inside Roostack, a link back to adding one from a file; on the website, the download for offline use.

**Import.** The Compose file on the left, and on the right **What came across**, in four groups, each item saying why:

- **Converted:** the image and version, the web address, the database (a Postgres service becomes a database Roostack runs and backs up, and the app's variables pointing at it become its details and password), generated secrets, storage, `depends_on`.
- **Not allowed:** what Roostack never gives a container (here the Docker socket), with what that means for the app.
- **Not needed in Roostack:** what Roostack does itself (restarts, the database's own volume and password, its health check).
- **Over to you:** what the import can't know: the image's digest (with the `skopeo` command: nothing is fetched), memory, what the storage holds, where people sign in, the project's facts.

**Edit.** Above both panes, **Problems**: counts of validation errors (what `roostack validate` refuses), registry errors (its package policy) and suggestions, then each problem with its field, its line and what to do. **For my own lab only** ("allows registry errors, for local use only") turns registry errors into warnings, as `validate -local` does; on the website it starts off, inside Roostack on. Clicking a problem opens its field in the form and its line in the YAML.

Below, the **form** and the **YAML**, side by side, both editable and kept in step (comments and order kept: the YAML is the document, and the form edits it in place).

- The form follows **Write a package**'s order (name and version, the project, containers, data, databases, web address, settings and setup, more), with an index at the top marking the parts with problems.
- **Help opens under the field you're on**: what it means in a sentence or two, the suggestions you can take as chips ("Use `AGPL-3.0-or-later`", "Use `/login`"), how to find the right value yourself, and a link to the help page. Try Licence, Storage and Sign-in.
- **Smaller suggestions** sit beside their field with a spark icon (categories from the name, the releases address from the source, a health check, keeping `/admin` home-only).
- The YAML marks lines with problems in the gutter (red: a validation error; amber: a registry error), and the field you're on.

**Phones:** the form, the YAML and the problems become three tabs, the problems' count on their tab. *Changed (2026-10-20, the Owner):* the tab bar comes first and the problems under it, on their own tab; at first the problems' counts sat above the tab bar on every tab.

**On the website and from a file:** no sidebar, no Check or Add; the description says nothing is sent anywhere. On roostack.cloud the editor sits in the site's own frame (its header and footer, dark), which the mock-up only suggests.

## Questions for the Owner

1. **The layout:** problems above, form and YAML side by side, help under the field. Considered: the help in a third column (always in the same place, but squeezes the form on a laptop), and problems in a column beside the YAML.
2. **The words:** "Before it can be added", "Roostack would refuse", "the registry would refuse", "For my own lab only"; the import's four groups.
3. **Add to this lab…:** sends the package and the logo chosen under The project to `/catalog/add`, which checks it and goes to the catalog as today. Or should it open the install page straight away?
4. **Two new icons**, for `icons.go`: import (a tray with an arrow) and spark (a suggestion).

## The Owner's answers (2026-10-20)

1. The layout: as shown.
2. The words: **Problems** for the strip, with **Validation errors**, **Registry errors** and **Suggestions**; the switch says "For my own lab only (allows registry errors, for local use only)"; the import's four groups as shown.
3. **Add to this lab…** goes to the catalog as adding from a file does, with a notice there saying it was added and linking to install it.
4. The two icons: yes.
