# Website design language: mint

The site reads like a quiet terminal. The mascot (`MASCOT.md`) lives inside
this look.

## Principles

- **One look.** Always dark; no light mode and no `prefers-color-scheme`
  switch. `color-scheme: dark` so scrollbars and form controls match.
- **One accent, used scarcely.** Mint marks only the block cursor, link
  underlines, list markers and the mascot. Nothing else is coloured.
- **The cursor is the signature.** A mint block follows the `bot` wordmark
  and ends the home page's commands. It blinks, and holds still under
  `prefers-reduced-motion: reduce`.
- **The machine speaks mono; people read sans.** Martian Mono for everything
  the system says and for the site's frame; Instrument Sans only for running
  prose in the docs.
- **No JavaScript except the optional home-page mascot, no external
  requests.** Fonts are served from the site; the mascot is specified in
  `MASCOT.md`, and every page is complete without it.

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
| `--mint` | `#5cf2a0` | Cursor, link underlines, list markers, current-page marker, the mascot |
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

One centred column, max width 1120px, generous vertical space. The first
block is a row: items 1–3 on the left, the mascot (square, up to 360px and 30% of the viewport width) on the right;
under 800px the mascot moves above the headline at 240×240px.

1. Headline: "an always-on agent with one tool: **the shell**." The first
   clause in `--dim`, "the shell." in `--fg`.
2. The README's introduction paragraph in `--dim`, max 60ch.
3. The example commands as a plain `pre` (no background): `#` prompts in
   `--dim`, ending in a prompt and the mint cursor.
4. The philosophy's three ideas as a numbered list, max width 760px: numbers `01`–`03` in
   `--dim`, the bold lead of each idea in `--fg`, the rest in `--dim`.
5. Footer: the version in `--dim`.

### Docs

- Side navigation (left margin, at least 220px): page names in `--dim`, the current page in
  `--fg` with a mint `▸`, and its h2s nested under a 1px `--line` rule.
  Collapses to a wrapped row above the article under 800px.
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
- Source path at the end of the article in mono `--dim`, linking to GitHub.

### Favicon

A 16×16 SVG: `--bg` rounded square with a mint block cursor.
