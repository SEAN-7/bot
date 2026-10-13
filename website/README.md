# Website

Static website built from the repo's markdown: the home page, plus docs
rendered from `README.md` (Guide), `bot/docs/philosophy.md`,
`bot/docs/spec.md` and `bot/internal/notes/notes.md` (Release notes). Edit
those files and the site follows; the `pages` workflow rebuilds and deploys
on every push to `main` that touches them.

This is its own Go module, standard library only. GitHub's markdown API
renders each document exactly as github.com shows it, with HTML sanitised
and GitHub's heading slugs, so building needs network access. Set
`GITHUB_TOKEN` to authenticate; without it the API allows 60 requests an
hour, enough for several builds.

Local preview, from this directory:

```bash
go run . -out /tmp/site -serve localhost:8000
```

Pages are listed in `pages` in `main.go`; links between them are rewritten,
other relative links point at the file on GitHub, and relative images are
copied. No JavaScript except the home page's optional mascot, a glyph swarm
specified in `MASCOT.md` and driven by `mascot/forms.json`, a definition a
later terminal version will share.

The look is specified in `DESIGN.md`. The site serves its own fonts from
`fonts/`: Latin subsets of Martian Mono and Instrument Sans, both under the
SIL Open Font License, with their licences beside them.
