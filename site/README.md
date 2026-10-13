# Site

Static website built from the repo's markdown: the home page, plus docs
rendered from `README.md` (Guide), `docs/philosophy.md`, `docs/spec.md` and
`internal/notes/notes.md` (Release notes). Edit those files and the site
follows; the `pages` workflow rebuilds and deploys on every push to `main`.

Local preview:

```bash
pip install markdown-it-py
python site/build.py /tmp/site && python -m http.server -d /tmp/site
```

Pages are listed in `PAGES` in `build.py`; links between them are rewritten,
other relative links point at the file on GitHub. No JavaScript.
