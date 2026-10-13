# Mascot: the glyph swarm

A faceless swarm of Martian Mono characters that takes any form, shaded by
depth. Its home form is **listen**, a slowly turning, breathing sphere. The
web version (`mascot/mascot.js`) and a later terminal version read the same
definition, `mascot/forms.json`, so they cannot drift.

## Behaviour on the home page

- **Arrival.** On load the particles start scattered (a cloud scaled by
  `scatter`) and assemble into listen, each leaving at a random moment
  within `stagger` seconds.
- **Listen** is the resting state: it turns, breathes, and leans toward the
  pointer anywhere on the page, by `pointer.lean` radians (yaw, pitch) per
  canvas width or height of pointer offset, up to half a canvas. Particles within `pointer.reach` (90) px of the pointer are pushed away
  from it with strength `pointer.push`.
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

## The shared definition: `forms.json`

Everything that decides how the mascot looks and moves is data in this file;
code in each language only implements the shape generators, the physics step
and the renderer. Keys named below are keys of that file.

### Determinism

Both implementations use the same PRNG, mulberry32, seeded with `seed`, and
consume it in the same order: the forms listed in `order`, each generated
then shuffled (Fisher–Yates from the last index down), then the arrival
scatter (cloud points scaled by `scatter`). A `speak` form is generated
when it is first needed, from the PRNG's state at that moment. With the same particle count, the same seed gives the
same layout in both languages, so a later cross-language test can compare
them point for point. `mascot.js` is the reference for the exact order of
draws within each shape. Only layout uses this PRNG: timing, stagger and
jitter use each language's ordinary random source.

### Shapes

All points are in model units, about one unit across, centred on the origin.

- **sphere**: Fibonacci sphere of `radius`; point *i* of *n* at
  `y = 1 − 2(i + 0.5)/n`, angle `i × 2.399963`.
- **cloud**: rejection-sampled points inside the unit ball, scaled by
  `radii`.
- **cube**: `1 − edges` of the points uniformly on the faces (face
  `⌊6 × rand⌋`, then the two in-face coordinates), the rest along the 12
  edges in turn.
- **bud**: a sphere for the parent and one for the child (`[x, y, z, r]`,
  with `share` of the points each), the rest on the thread between their
  centres, each offset by up to ±`thread`/2 in every axis.
- **word**: the text drawn with `font`; each lit pixel is a `pixel`-unit
  square, scaled down so the word is at most `fit` units wide; points are
  dealt to lit pixels in turn, each placed randomly inside its square and `depth` units deep.

### Motion

Per frame, for particle *i*, the target is its form point, then:
`breathe` scales it by `1 + breathe × sin(2.1t)`; `wander` (with
`φ = 1.618i`) adds `x += 0.22 sin(0.55t + 3.1φ) + 0.18 sin(0.37t + 2y)`,
then `y += 0.16 sin(0.71t + 2.3φ) + 0.12 cos(0.5t + 1.7x)`, then
`z += 0.2 cos(0.45t + 1.7φ)`, all scaled by `wander`; `jitter` adds random noise of that size; the form
turns by `angle − lean` (yaw, `angle` growing at `spin` rad/s, `sway` adding
`sway × sin(0.6t)`) and `camera.pitch − leanY` (pitch), which turns the near
side toward the pointer, then shifts by `0.12 × lean` across and
`−0.12 × leanY` up, following it; `flat` forms face the viewer and only
shift. Each particle moves toward its target as a damped spring:
`v += ((target − p) × k − v × damping) × dt`, then `p += v × dt`.
Projection is perspective with `camera.distance`; on the web one model unit
is 0.3 of the canvas's shorter side, and characters fade out within 24px of
the canvas edge so no form shows a hard cut. Brightness
`b = clamp((1.1 − z) / 2.2)` picks the ramp character (or `dense` for a
`flat` form), the size, and the colour (`light` above `lightAbove`).
