// The glyph swarm: see MASCOT.md. Every form parameter comes from forms.json,
// which main.go inlines on the home page; this file only implements the
// shape generators, the physics step and the renderer.
(() => {
  const canvas = document.querySelector('canvas.swarm');
  const data = document.getElementById('mascot-forms');
  const ctx = canvas && canvas.getContext && canvas.getContext('2d');
  if (!ctx || !data) return;
  let F;
  try { F = JSON.parse(data.textContent); } catch (e) { return; }

  const { ramp, dense, colors, camera, ambient, pointer } = F;
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const N = innerWidth < F.particles.smallBelow ? F.particles.small : F.particles.large;

  // Page behaviour that is not part of a form, so not in forms.json.
  const SIZE = 0.36;            // model unit in canvas pixels, as a share of the canvas's shorter side

  // mulberry32, used only for layout, in the order MASCOT.md § Determinism fixes;
  // timing, stagger and jitter use Math.random.
  const mulberry32 = a => () => {
    a = a + 0x6D2B79F5 | 0;
    let t = Math.imul(a ^ a >>> 15, 1 | a);
    t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
  const rand = mulberry32(F.seed);
  const between = ([lo, hi]) => lo + Math.random() * (hi - lo);
  const clamp = (v, lo = 0, hi = 1) => Math.max(lo, Math.min(hi, v));
  const shuffle = a => {
    for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(rand() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; }
    return a;
  };

  // Shapes: each returns n points in model units; (n, shape, text) so they dispatch alike.
  const fibonacci = (n, r, c) => Array.from({ length: n }, (_, i) => {
    const y = 1 - 2 * (i + 0.5) / n, rad = Math.sqrt(1 - y * y), th = i * 2.399963;
    return [c[0] + Math.cos(th) * rad * r, c[1] + y * r, c[2] + Math.sin(th) * rad * r];
  });
  function cloud(n, { radii }) {
    const out = [];
    while (out.length < n) {
      const p = [rand() * 2 - 1, rand() * 2 - 1, rand() * 2 - 1];
      if (p[0] * p[0] + p[1] * p[1] + p[2] * p[2] <= 1) out.push(p.map((v, k) => v * radii[k]));
    }
    return out;
  }
  function cube(n, { half: s, edges }) {
    const out = [];
    for (let i = Math.round(n * (1 - edges)); i > 0; i--) { // faces: equal areas, so equal odds
      const face = Math.min(5, Math.floor(rand() * 6)), u = rand() * 2 - 1, v = rand() * 2 - 1, w = face & 1 ? -s : s;
      out.push([[w, u * s, v * s], [u * s, w, v * s], [u * s, v * s, w]][face >> 1]);
    }
    const corner = c => [c & 4 ? s : -s, c & 2 ? s : -s, c & 1 ? s : -s], rim = [];
    for (let a = 0; a < 8; a++) for (let b = a + 1; b < 8; b++) if ([1, 2, 4].includes(a ^ b)) rim.push([corner(a), corner(b)]);
    while (out.length < n) { // the 12 edges in turn
      const [a, b] = rim[out.length % rim.length], t = rand();
      out.push(a.map((v, k) => v + (b[k] - v) * t));
    }
    return out;
  }
  function bud(n, { parent: p, child: c, share, thread }) {
    const out = fibonacci(Math.round(n * share[0]), p[3], p).concat(fibonacci(Math.round(n * share[1]), c[3], c));
    while (out.length < n) { // the thread between the two centres
      const t = rand();
      out.push([0, 1, 2].map(k => p[k] + (c[k] - p[k]) * t + (rand() - 0.5) * thread));
    }
    return out;
  }
  function word(n, { pixel, fit, depth }, text) {
    const { width: gw, height: gh, glyphs } = F.font, chars = [...text.toLowerCase()], lit = [];
    chars.forEach((ch, li) => (glyphs[ch] || []).forEach((row, y) => {
      [...row].forEach((c, x) => { if (c === '#') lit.push([li * (gw + 1) + x, y]); });
    }));
    if (!lit.length) return states[F.home].pts.slice(); // nothing to write: keep to the home form
    const cols = chars.length * (gw + 1) - 1, s = Math.min(pixel, fit / cols), out = [];
    for (let i = 0; out.length < n; i++) {
      const [x, y] = lit[i % lit.length];
      out.push([(x + rand() - cols / 2) * s, -(y + rand() - gh / 2) * s, (rand() - 0.5) * depth]);
    }
    return out;
  }
  const shapes = { sphere: (n, { radius }) => fibonacci(n, radius, [0, 0, 0]), cloud, cube, bud, word };

  // A form is its config, its shuffled points and its turning angle. The
  // ones in `order` exist up front; `speak:WORD` forms are made on first use.
  const states = {};
  function formFor(key) {
    if (!states[key]) {
      const [name, text] = key.split(':'), cfg = F.forms[name];
      states[key] = { cfg, pts: shuffle(shapes[cfg.shape.type](N, cfg.shape, text || '')), angle: 0 };
    }
    return states[key];
  }
  F.order.forEach(formFor);

  // Particles start scattered; each is released into the home form at a random moment.
  const px = new Float32Array(N), py = new Float32Array(N), pz = new Float32Array(N);
  const vx = new Float32Array(N), vy = new Float32Array(N), vz = new Float32Array(N);
  const sx = new Float32Array(N), sy = new Float32Array(N); // last drawn screen position
  cloud(N, { radii: F.scatter }).forEach((p, i) => { px[i] = p[0]; py[i] = p[1]; pz[i] = p[2]; });
  const from = new Array(N).fill(null), switchAt = new Float32Array(N), depth = new Uint16Array(N).map((_, i) => i);
  let cur = formFor(F.home), t = 0;
  for (let i = 0; i < N; i++) switchAt[i] = reduce ? 0 : Math.random() * F.stagger;
  const formOf = i => t >= switchAt[i] ? cur : from[i];
  function go(key) {
    const next = formFor(key);
    for (let i = 0; i < N; i++) {
      if (t >= switchAt[i]) from[i] = cur;
      switchAt[i] = t + Math.random() * F.stagger;
    }
    cur = next;
  }

  // Scheduler: ambient forms in turn, and drifting when the pointer is idle.
  let lastMove = 0, idle = false, turn = 0, until = 0, next = between(ambient.every);
  function schedule() {
    if (!idle && t - lastMove > ambient.idleAfter) { idle = true; until = 0; go('drift'); }
    if (idle) return;
    if (!until && t >= next) { go(ambient.sequence[turn++ % ambient.sequence.length]); until = t + ambient.hold; }
    else if (until && t >= until) { go(F.home); until = 0; next = t + between(ambient.every); }
  }

  // Pointer: the swarm leans toward it anywhere on the page and scatters around it.
  let ptr = null, lean = 0, leanY = 0;
  function onMove(e) {
    ptr = { x: e.clientX, y: e.clientY };
    lastMove = t;
    if (idle) { idle = false; go(F.home); next = t + between(ambient.every); }
  }
  const onLeave = () => { ptr = null; };

  // Where particle i wants to be: its form point, breathing, wandering,
  // jittering, then turned by the form's angle and the lean.
  const aim = [0, 0, 0];
  function target(s, i) {
    const c = s.cfg, phi = 1.618 * i;
    let [x, y, z] = s.pts[i];
    if (c.breathe) { const b = 1 + c.breathe * Math.sin(2.1 * t); x *= b; y *= b; z *= b; }
    if (c.wander && !reduce) {
      x += c.wander * (0.22 * Math.sin(0.55 * t + 3.1 * phi) + 0.18 * Math.sin(0.37 * t + 2 * y));
      y += c.wander * (0.16 * Math.sin(0.71 * t + 2.3 * phi) + 0.12 * Math.cos(0.5 * t + 1.7 * x));
      z += c.wander * 0.2 * Math.cos(0.45 * t + 1.7 * phi);
    }
    if (c.jitter && !reduce) { x += (Math.random() - 0.5) * c.jitter; y += (Math.random() - 0.5) * c.jitter; z += (Math.random() - 0.5) * c.jitter; }
    const yaw = c.flat ? 0 : s.angle + lean + (c.sway ? c.sway * Math.sin(0.6 * t) : 0);
    const pitch = c.flat ? 0 : camera.pitch + leanY;
    const cy = Math.cos(yaw), sn = Math.sin(yaw), cp = Math.cos(pitch), sp = Math.sin(pitch);
    const x1 = cy * x + sn * z, z1 = -sn * x + cy * z;
    aim[0] = x1; aim[1] = cp * y - sp * z1; aim[2] = sp * y + cp * z1;
  }

  function step(dt) {
    if (!scale) return; // the canvas has no size yet
    t += dt;
    schedule();
    let tx = 0, ty = 0, r = null;
    if (ptr) {
      r = canvas.getBoundingClientRect();
      tx = clamp((ptr.x - r.left - r.width / 2) / r.width, -0.5, 0.5) * pointer.lean[0];
      ty = clamp((ptr.y - r.top - r.height / 2) / r.height, -0.5, 0.5) * pointer.lean[1];
    }
    lean += (tx - lean) * Math.min(1, dt * 3);
    leanY += (ty - leanY) * Math.min(1, dt * 3);
    for (const k in states) states[k].angle += (states[k].cfg.spin || 0) * dt;
    for (let i = 0; i < N; i++) {
      const s = formOf(i);
      if (!s) continue; // not yet released from the scatter
      target(s, i);
      const k = s.cfg.k, d = s.cfg.damping;
      vx[i] += ((aim[0] - px[i]) * k - vx[i] * d) * dt;
      vy[i] += ((aim[1] - py[i]) * k - vy[i] * d) * dt;
      vz[i] += ((aim[2] - pz[i]) * k - vz[i] * d) * dt;
      if (r) {
        const dx = sx[i] - (ptr.x - r.left), dy = sy[i] - (ptr.y - r.top), dist = Math.hypot(dx, dy);
        if (dist < pointer.reach && dist > 0.01) {
          const push = (1 - dist / pointer.reach) * pointer.push * dt * 60 / scale;
          vx[i] += dx / dist * push; vy[i] -= dy / dist * push;
        }
      }
      px[i] += vx[i] * dt; py[i] += vy[i] * dt; pz[i] += vz[i] * dt;
    }
  }

  // Canvas size follows its CSS box (square), at up to twice the pixel ratio.
  let w = 0, h = 0, scale = 0, ready = false;
  function measure() {
    const r = canvas.getBoundingClientRect(), dpr = Math.min(2, devicePixelRatio || 1);
    w = r.width; h = r.height; scale = SIZE * Math.min(w, h);
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
    if (ready) draw();
  }

  function draw() {
    ctx.clearRect(0, 0, w, h);
    if (!scale) return;
    const dist = camera.distance;
    depth.sort((a, b) => pz[b] - pz[a]); // far to near
    let font = '';
    for (const i of depth) {
      const persp = dist / (dist + pz[i]), b = clamp((1.1 - pz[i]) / 2.2), s = formOf(i);
      sx[i] = w / 2 + px[i] * scale * persp;
      sy[i] = h / 2 - py[i] * scale * persp;
      const chars = s && s.cfg.flat ? dense : ramp;
      const f = `400 ${Math.round((6 + 9 * b) * persp * scale / 240 + 3)}px "Martian Mono", ui-monospace, monospace`;
      if (f !== font) { ctx.font = f; font = f; }
      ctx.globalAlpha = 0.16 + 0.84 * b;
      ctx.fillStyle = b > colors.lightAbove ? colors.light : colors.mint;
      ctx.fillText(s && s.cfg.flat ? chars[i % chars.length] : chars[Math.round(b * (chars.length - 1))], sx[i], sy[i]);
    }
    ctx.globalAlpha = 1;
  }

  // The loop runs only while the canvas is on screen, the tab is visible and the font is loaded.
  let onScreen = false, frame = 0, last = 0;
  function tick(now) {
    frame = requestAnimationFrame(tick);
    step(last ? Math.min(0.05, (now - last) / 1000) : 0);
    last = now;
    draw();
  }
  function sync() {
    const run = ready && onScreen && !document.hidden;
    if (run && !frame) frame = requestAnimationFrame(tick);
    else if (!run && frame) { cancelAnimationFrame(frame); frame = last = 0; }
  }

  measure();
  if (window.ResizeObserver) new ResizeObserver(measure).observe(canvas);
  const fontLoaded = document.fonts ? document.fonts.load('400 12px "Martian Mono"').catch(() => {}) : Promise.resolve();
  if (reduce) { // one still frame of the home form
    fontLoaded.then(() => {
      ready = true;
      for (let i = 0; i < N; i++) { target(cur, i); px[i] = aim[0]; py[i] = aim[1]; pz[i] = aim[2]; }
      draw();
    });
    return;
  }
  addEventListener('pointermove', onMove, { passive: true });
  addEventListener('blur', onLeave);
  document.documentElement.addEventListener('pointerleave', onLeave);
  document.addEventListener('visibilitychange', sync);
  if (window.IntersectionObserver) {
    new IntersectionObserver(es => { onScreen = es[es.length - 1].isIntersecting; sync(); }).observe(canvas);
  } else onScreen = true;
  fontLoaded.then(() => { ready = true; sync(); });
})();
