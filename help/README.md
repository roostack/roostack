# Roostack Help

The help site, built with [Zensical](https://zensical.org) (SPEC.md §6.3.1).
Pages are Markdown in `docs/`; the navigation is in `zensical.toml`.

```sh
uv run zensical serve    # preview at http://localhost:8000, rebuilt on save
uv run zensical build    # the site, in site/
```

`make build` (at the top of the repo) builds it strictly (a broken link
fails the build) and embeds it in Roostack, served at `/help/`.

Writing: plain language for the person running Roostack, British English,
short sentences. Exact details (paths, commands, logs) go in a collapsed
section at the end of the page:

```markdown
??? note "For technical users"

    ...
```

Check facts against the code and templates (labels, paths, schedules), and
commands on a dev VM, before writing them down.
