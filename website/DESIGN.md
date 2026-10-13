# Website design language: mint

The site's look, decided 2026-10-13 from four explored directions (literate
machine, phosphor, field guide, Swiss systems). It is a minimal take on the
phosphor direction: the site reads like a quiet terminal. This file is the
reference for the site's look; the mascot (a later project) is designed
inside it.

## Principles

- **One look.** Always dark; no light mode and no `prefers-color-scheme`
  switch. `color-scheme: dark` so scrollbars and form controls match.
- **One accent, used scarcely.** Mint marks only the block cursor, link
  underlines and list markers. Nothing else is coloured.
- **The cursor is the signature.** A mint block follows the `bot` wordmark
  and ends the home page's commands. It blinks, and holds still under
  `prefers-reduced-motion: reduce`. The mascot will later live in the home
  page's cursor.
- **The machine speaks mono; people read sans.** Martian Mono for everything
  the system says and for the site's frame; Instrument Sans only for running
  prose in the docs.
- **No JavaScript, no external requests.** Fonts are served from the site.

## Colour

| Token | Value | Use |
| --- | --- | --- |
| `--bg` | `#0d1110` | Page ground |
| `--fg` | `#e6efe9` | Primary text, headings, the emphasised phrase |
| `--prose` | `#c3cec8` | Running prose in the docs |
| `--dim` | `#7f8d86` | Secondary text, navigation, prompts, footer |
| `--faint` | `#3f4d47` | The `##` marker before headings |
| `--line` | `#2a3531` | Table header rule, side-navigation rule |
| `--rule` | `#1c2522` | Table row rules |
| `--code` | `#161d1b` | Code and pre background |
| `--mint` | `#5cf2a0` | Cursor, link underlines, list markers, current-page marker |
| `--strong` | `#ffffff` | `strong` in prose |

`--dim` on `--bg` is at least 4.5:1. Mint is never used for body text.

## Type

- **Martian Mono** (variable, weights 100–800, width 75–112.5). Home page,
  header, navigation, footer, headings, `code`, `pre`, tables, labels.
  Weight 300 by default, 600 for the wordmark. Lowercase for our own
  interface words (`docs`, `source`, `guide`); document titles keep their
  case.
- **Instrument Sans** (variable, weights 400–700). Paragraphs and list items
  in docs articles, 17px, line height 1.75, weight 400.
- Both are under the SIL Open Font License. The site ships each as a
  Latin-subset woff2 in `website/fonts/` with its `OFL.txt`, embedded in the
  build like `style.css` and declared with `@font-face` and
  `font-display: swap`. Fallbacks: `ui-monospace, monospace` and
  `system-ui, sans-serif`.

Sizes (desktop; phone sizes scale down in one media query):

| Element | Font | Size |
| --- | --- | --- |
| Home headline | Mono 300, letter-spacing −0.02em | 44px, line height 1.25 |
| Home body, ideas | Mono 300 | 15px, line height 1.8 |
| Home commands | Mono 300 | 14px |
| Header, navigation | Mono | 13px |
| Doc title (h1) | Mono 400 | 34px |
| Doc h2 | Mono 400, `## ` prefix in `--faint` | 20px |
| Doc h3 | Mono 400 | 16px |
| Doc prose | Instrument Sans 400 | 17px |
| Code, tables | Mono 300 | 0.82–0.86em |
| Footer, source path | Mono | 11–12px |

## Layout

### Header (every page)

`bot` wordmark (mono 600) followed by the mint cursor, left; `docs` and
`source` links in `--dim`, right; the current section in `--fg`.

### Home

One centred column, max width 920px, generous vertical space:

1. Headline: "an always-on agent with one tool: **the shell**." The first
   clause in `--dim`, "the shell." in `--fg`.
2. The README's introduction paragraph in `--dim`, max 60ch.
3. The example commands as a plain `pre` (no background): `#` prompts in
   `--dim`, ending in a prompt and the mint cursor.
4. The philosophy's three ideas as a numbered list: numbers `01`–`03` in
   `--dim`, the bold lead of each idea in `--fg`, the rest in `--dim`.
5. Footer: the version in `--dim`.

Content stays sourced as today (README introduction, philosophy's three
ideas, version from the release notes); only the markup and styles change.

### Docs

- Side navigation (left margin, at least 220px): page names in `--dim`, the current page in
  `--fg` with a mint `▸`, and its h2s nested under a 1px `--line` rule.
  Collapses to a wrapped row above the article under 800px, as today.
- Article: max width 760px, centred on the page like the home column. The
  page is a three-column grid, `minmax(220px, 1fr) minmax(0, 760px) 1fr`:
  side navigation in the left margin, the article in the middle, the right
  margin empty. h1, h2, h3 in mono; h2 prefixed `## ` in
  `--faint`. Prose in Instrument Sans `--prose`; `strong` in `--strong`.
  List markers in mint. Links `--fg` with a mint underline; hover turns the
  text mint.
- Tables: no outer border, header in mono `--dim`, `--line` under the
  header, `--rule` between rows, horizontal scroll when wide.
- Code: inline `code` on `--code` with a 3px radius; `pre` on `--code`,
  4px radius, padding 18px 20px.
- Source path at the end of the article in mono `--dim`, linking to GitHub,
  as today.

### Favicon

A 16×16 SVG: `--bg` rounded square with a mint block cursor, replacing the
current `>_` mark.

## Implementation

All changes are in `website/`; the markdown pipeline (GitHub's API, link and
heading rewriting, asset copying) does not change.

- `style.css`: rewritten to the tokens and rules above.
- `main.go`: the `layout` and `home` templates updated to the structure
  above; `fonts/*.woff2` added to the embedded files and written to
  `fonts/` in the output.
- `fonts/`: `MartianMono.woff2`, `InstrumentSans.woff2` and both licences.
- `favicon.svg`: redrawn.
- `main_test.go`: `TestBuild` also checks that the fonts are written.
- `README.md`: notes the bundled fonts and their licences; "No JavaScript"
  stays true.

Verification: `go vet ./...` and `go test ./...` in `website/`, then a local
build with `go run . -out <tmp> -serve localhost:8000`, checked in a browser
at desktop and phone widths.

## Out of scope

The mascot, which is its own project and spec, and any change to which
documents the site publishes.
