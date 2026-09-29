# Built help site

`make build` builds the help site (`help/`, SPEC.md §6.3.1) into `site/`
here, and the web UI embeds it and serves it at `/help/` (help.go). Only
this file is committed: without a built site, `go build` and the tests
still work, and `/help/` says the help isn't included.
