# Mascot: the glyph swarm

The site's mascot, decided 2026-10-14 after exploring a ghost, a voxel figure,
a vector wireframe, eyes and faced ASCII creatures. It has no face: it is a
swarm of Martian Mono characters that takes any form, shaded by depth with
the `.,:;~=!*#$@` ramp. Its home form is **listen**, a slowly turning,
breathing sphere. It lives inside the design language in `DESIGN.md`.

This project puts it on the website. A terminal version follows later; this
spec fixes the shared definition both read, so the two cannot drift.

## Behaviour on the home page

- **Arrival.** On load the particles start scattered and assemble into
  listen, each leaving at a random moment within `stagger` seconds.
- **Listen** is the resting state: it turns, breathes, and leans toward the
  pointer anywhere on the page. Particles within 90px of the pointer are
  pushed away from it.
- **Ambient forms.** Every 18–24 seconds it takes one form from the ambient
  sequence (`drift`, `work`, `spawn`, `speak:hello`, in turn), holds it for 4
  seconds, and returns to listen.
- **Idle.** After 60 seconds without pointer movement it drifts until the
  pointer moves again, then returns to listen.
- **Decorative.** No controls; the canvas is `aria-hidden="true"` and takes
  no focus.
- **Reduced motion** (`prefers-reduced-motion: reduce`): one still frame of
  listen, drawn once, no animation, no ambient forms.
- **No JavaScript**: the space stays empty; the page is complete without it.
- **Cost.** It pauses while off-screen (`IntersectionObserver`) or while the
  tab is hidden, and caps the canvas pixel ratio at 2.

## Layout

The home column widens from 920px to 1120px and its first block becomes a
row: the existing headline, introduction and commands on the left
(`flex: 1 1 480px`, `min-width: 0`), the swarm on the right (360×360px). The
three ideas stay below, in the left column's width.

Under 800px the swarm moves above the headline, centred, 240×240px.

The swarm uses mint and a lighter mint, `#c8ffe1`, for the nearest
characters. `DESIGN.md`'s list of where mint may appear gains "the mascot".

## The shared definition: `website/mascot/forms.json`

Everything that decides how the mascot looks and moves is data in this one
file. The web script reads it now; the terminal version reads the same file
later. Code in each language only implements the shape generators, the
physics step and the renderer.

```json
{
  "version": 1,
  "seed": 7,
  "particles": { "small": 420, "large": 720, "smallBelow": 600 },
  "ramp": ".,:;~=!*#$@",
  "dense": "@#$%&*8",
  "colors": { "mint": "#5cf2a0", "light": "#c8ffe1", "lightAbove": 0.82 },
  "camera": { "distance": 3.4, "pitch": 0.2 },
  "stagger": 0.7,
  "home": "listen",
  "ambient": { "every": [18, 24], "hold": 4, "idleAfter": 60,
               "sequence": ["drift", "work", "spawn", "speak:hello"] },
  "order": ["listen", "drift", "work", "spawn"],
  "forms": {
    "listen": { "shape": { "type": "sphere", "radius": 0.85 },
                "spin": 0.3, "breathe": 0.045, "k": 34, "damping": 8.5 },
    "drift":  { "shape": { "type": "cloud", "radii": [1.5, 0.7, 1.1] },
                "spin": 0.12, "wander": 1, "k": 18, "damping": 6 },
    "work":   { "shape": { "type": "cube", "half": 0.62, "edges": 0.45 },
                "spin": 0.9, "jitter": 0.012, "k": 40, "damping": 9 },
    "spawn":  { "shape": { "type": "bud", "parent": [-0.42, -0.05, 0, 0.78],
                           "child": [0.95, 0.3, 0, 0.34], "share": [0.72, 0.2] },
                "sway": 0.45, "k": 30, "damping": 8 },
    "speak":  { "shape": { "type": "word", "pixel": 0.13, "fit": 3.0 },
                "flat": true, "k": 34, "damping": 9 }
  },
  "font": { "width": 5, "height": 7, "glyphs": { "a": [".....", "....."] } }
}
```

(`font.glyphs` is abbreviated here; the file holds the full 5×7 lowercase
font `a`–`z` plus `.`, `!`, `?` and space.)

### Determinism

Both implementations use the same PRNG, mulberry32, seeded with `seed`, and
consume it in the same order: the forms listed in `order`, each generated
then shuffled (Fisher–Yates from the last index down), then the arrival
scatter. A `speak` form is generated when it is first needed, from the PRNG's
state at that moment. With the same particle count, the same seed gives the
same layout in both languages, so a later cross-language test can compare
them point for point.

### Shapes

All points are in model units, about one unit across, centred on the origin.

- **sphere**: Fibonacci sphere of `radius`; point *i* of *n* at
  `y = 1 − 2(i + 0.5)/n`, angle `i × 2.399963`.
- **cloud**: rejection-sampled points inside the unit ball, scaled by
  `radii`.
- **cube**: `1 − edges` of the points uniformly on the faces (face chosen by
  area), the rest along the 12 edges in turn.
- **bud**: a sphere for the parent and one for the child (`[x, y, z, r]`,
  with `share` of the points each), the rest on the thread between their
  centres.
- **word**: the text drawn with `font`; each lit pixel is a `pixel`-unit
  square, scaled down so the word is at most `fit` units wide; points are
  dealt to lit pixels in turn, each placed randomly inside its square.

### Motion

Per frame, for particle *i*, the target is its form point, then:
`breathe` scales it by `1 + breathe × sin(2.1t)`; `wander` (with
`φ = 1.618i`) adds `x += 0.22 sin(0.55t + 3.1φ) + 0.18 sin(0.37t + 2y)`,
then `y += 0.16 sin(0.71t + 2.3φ) + 0.12 cos(0.5t + 1.7x)`, then
`z += 0.2 cos(0.45t + 1.7φ)`, all scaled by `wander`; `jitter` adds random noise of that size; the form
turns by `angle + lean` (yaw, `angle` growing at `spin` rad/s, `sway` adding
`sway × sin(0.6t)`) and `camera.pitch + leanY` (pitch); `flat` forms face the
viewer. Each particle moves toward its target as a damped spring:
`v += ((target − p) × k − v × damping) × dt`, then `p += v × dt`.
Projection is perspective with `camera.distance`. Brightness
`b = clamp((1.1 − z) / 2.2)` picks the ramp character (or `dense` for a
`flat` form), the size, and the colour (`light` above `lightAbove`).

## Files

- `website/mascot/forms.json`: the shared definition.
- `website/mascot/mascot.js`: the web implementation, hand-written, no
  libraries, about 250 lines.
- `website/main.go`: embeds both; inlines `forms.json` into the home page as
  `<script type="application/json" id="mascot-forms">` (no extra request) and
  loads `mascot.js` with `defer` on the home page only.
- `website/style.css`, the `home` template: the layout above.
- `website/DESIGN.md`, `website/README.md`: "No JavaScript" becomes "no
  JavaScript except the optional home-page mascot"; the mint list gains the
  mascot.

## Testing

- Go: `TestBuild` checks that `mascot/mascot.js` is written, that the home
  page contains the canvas and the inlined `forms.json`, and that doc pages
  load neither. A test parses `forms.json` and checks every form named in
  `home`, `order` and `ambient.sequence` exists and every font glyph is 7
  rows of 5.
- Browser: home at 1440, 1024 and 375px: it assembles into listen, leans and
  scatters with the pointer, cycles an ambient form, pauses off-screen, holds
  one still frame under reduced motion, and leaves no console errors.

## Out of scope

The terminal version and where it lives (a separate tool, never the `agent`
binary); controls or interaction beyond the pointer; the cross-language test,
which arrives with the terminal version.
