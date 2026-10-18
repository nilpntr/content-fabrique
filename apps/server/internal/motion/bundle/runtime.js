// The motion-graphics runtime (docs/design/motion-graphics-v1.md section 3). It is loaded before any template and does three
// things:
//
//   1. It replaces the page's sense of time with a virtual clock. performance.now, Date, requestAnimationFrame and the timers
//      only move when the runner seeks, and Math.random is seeded by the job, so a frame is a function of its time alone:
//      not of how fast the machine is or of the frames rendered before it.
//   2. It mounts the job's overlay items: each item is a box on the stage, built by its template from its params. Templates
//      animate with the Web Animations API (element.animate) and may register a tick for what is a function of time but not a
//      CSS property (a number that counts). Every text goes into the DOM with textContent, never as markup.
//   3. It seeks: every animation of every item on screen is paused and set to the item's local time, the ticks run, and items
//      that are not on screen are hidden. The runner captures the frame after that.
//
// The ideas (a virtual clock, seeking paused animations instead of playing them) follow open-edit's page runtime
// (github.com/veedstudio/open-edit, Apache-2.0); this file is our own code. See THIRD_PARTY_NOTICES.md.
(() => {
  'use strict';
  if (window.__cfMotion) return;

  // ---- 1. The virtual clock -------------------------------------------------------------------------------------------
  const EPOCH = Date.UTC(2026, 0, 1); // Date.now() at time 0: a fixed instant, never the machine's clock.
  const RealDate = Date;
  let now = 0;

  Object.defineProperty(performance, 'now', { value: () => now, configurable: false, writable: false });

  function VirtualDate(...args) {
    if (!new.target) return new RealDate(EPOCH + now).toString();
    return Reflect.construct(RealDate, args.length ? args : [EPOCH + now], new.target);
  }
  VirtualDate.prototype = RealDate.prototype;
  VirtualDate.now = () => EPOCH + now;
  VirtualDate.parse = RealDate.parse;
  VirtualDate.UTC = RealDate.UTC;
  window.Date = VirtualDate;

  // xorshift32 (Marsaglia), seeded by the job: the same jitter on every render.
  let rng = 0x9e3779b9;
  function seedRandom(seed) { rng = (seed >>> 0) || 0x9e3779b9; }
  function random() {
    rng ^= rng << 13; rng >>>= 0;
    rng ^= rng >>> 17;
    rng ^= rng << 5; rng >>>= 0;
    return rng / 4294967296;
  }
  Math.random = random;

  // Timers and animation frames run only when the clock is moved, in order of their due time.
  let nextTimer = 1;
  const timers = new Map();
  function addTimer(fn, delay, args, repeat) {
    if (typeof fn !== 'function') throw new TypeError('timers take a function, not a string');
    const d = Math.max(repeat ? 1 : 0, Number(delay) || 0);
    const id = nextTimer++;
    timers.set(id, { id, due: now + d, fn, args, repeat: repeat ? d : 0 });
    return id;
  }
  window.setTimeout = (fn, delay, ...args) => addTimer(fn, delay, args, false);
  window.setInterval = (fn, delay, ...args) => addTimer(fn, delay, args, true);
  window.clearTimeout = (id) => { timers.delete(id); };
  window.clearInterval = (id) => { timers.delete(id); };
  let nextFrame = 1;
  let frames = new Map();
  window.requestAnimationFrame = (cb) => { const id = nextFrame++; frames.set(id, cb); return id; };
  window.cancelAnimationFrame = (id) => { frames.delete(id); };

  const problems = [];
  function report(where, e) {
    if (problems.length < 20) problems.push(where + ': ' + (e && e.message ? e.message : String(e)));
  }

  function advance(t) {
    // Timers due by t, earliest first; a timer that re-arms itself waits at least 1 ms, so this ends.
    for (let guard = 0; guard < 100000; guard++) {
      let next = null;
      for (const tm of timers.values()) {
        if (tm.due <= t && (!next || tm.due < next.due || (tm.due === next.due && tm.id < next.id))) next = tm;
      }
      if (!next) break;
      now = Math.max(now, next.due);
      if (next.repeat) next.due += next.repeat; else timers.delete(next.id);
      try { next.fn(...next.args); } catch (e) { report('timer', e); }
    }
    now = t;
    const due = frames;
    frames = new Map();
    for (const cb of due.values()) { try { cb(t); } catch (e) { report('animation frame', e); } }
  }

  // Nothing in a template needs the network, workers or other windows; they are taken away so a mistake fails loudly here
  // (the browser and the sandbox refuse them as well).
  const refuse = (name) => () => { throw new Error(name + ' is not available to templates'); };
  for (const name of ['fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource', 'Worker', 'SharedWorker', 'open']) {
    try { Object.defineProperty(window, name, { value: refuse(name), configurable: false, writable: false }); } catch (e) { /* not defined here */ }
  }
  try { Object.defineProperty(navigator, 'sendBeacon', { value: refuse('sendBeacon'), configurable: false }); } catch (e) { /* ignore */ }

  // ---- 2. Templates and mounting --------------------------------------------------------------------------------------
  const templates = new Map();
  function register(name, version, build) {
    if (typeof build !== 'function' || templates.has(name)) throw new Error('template ' + name + ' is registered twice or has no build function');
    templates.set(name, { version, build });
  }

  const COLOUR = /^(#[0-9A-Fa-f]{6}|rgba\(\d{1,3},\d{1,3},\d{1,3},(0|1|0\.\d{1,4})\))$/;
  function colour(c, what) {
    if (typeof c !== 'string' || !COLOUR.test(c)) throw new Error(what + ' is not a colour the runtime accepts');
    return c;
  }
  function px(n, what) {
    if (typeof n !== 'number' || !Number.isFinite(n) || n < -4000 || n > 4000) throw new Error(what + ' is not a size');
    return n + 'px';
  }

  // The context a template builds with. Every helper writes through the CSSOM or textContent: nothing is ever parsed as HTML.
  function context(item, root, ticks, fontsLoaded) {
    const p = item.params;
    if (!Array.isArray(p.lines) || p.lines.length < 1 || p.lines.length > 4) throw new Error('params.lines must hold 1 to 4 lines');
    if (!fontsLoaded.has(p.font)) throw new Error('font ' + p.font + ' was not loaded');
    const el = (tag, cls, parent) => {
      const e = document.createElement(tag);
      if (cls) e.className = cls;
      (parent || root).appendChild(e);
      return e;
    };
    const ctx = {
      p,
      box: item.box,
      random,
      el,
      px,
      colour,
      // text sets the text of an element. The only way a template writes words.
      text(e, s) { e.textContent = String(s); return e; },
      // anim runs keyframes on an element from start for dur ms (item-local), holding the first keyframe before and the last
      // after (fill both), or only the last (fill forwards) for an exit that must not show before it starts.
      anim(e, keyframes, start, dur, easing, fill) {
        return e.animate(keyframes, { delay: Math.max(0, start), duration: Math.max(1, dur), easing: easing || 'linear', fill: fill || 'both' });
      },
      // metrics measures the item's font as Chrome lays it out in a line of line_px: where the baseline is from the top of the line
      // and how tall the capitals are, in px. Decorations that sit on the letters (a marker, a rule) are placed with it.
      metrics() {
        const c = document.createElement('canvas').getContext('2d');
        c.font = px(p.font_px, 'font_px') + ' "' + p.font + '"';
        const m = c.measureText('H');
        const asc = m.fontBoundingBoxAscent;
        const desc = m.fontBoundingBoxDescent;
        return { baseline: (p.line_px - (asc + desc)) / 2 + asc, cap: m.actualBoundingBoxAscent };
      },
      // tick registers a function of the item's local time, called on every seek while the item is on screen.
      tick(fn) { ticks.push(fn); },
      // typeset applies the font, size, colours, shadow and outline of the params to an element.
      typeset(e) {
        e.style.fontFamily = '"' + p.font + '"';
        e.style.fontSize = px(p.font_px, 'font_px');
        e.style.lineHeight = px(p.line_px, 'line_px');
        e.style.color = colour(p.fill, 'fill');
        const shadows = [];
        if (p.shadow) shadows.push(px(p.shadow.x, 'shadow.x') + ' ' + px(p.shadow.y, 'shadow.y') + ' ' + px(p.shadow.blur || 0, 'shadow.blur') + ' ' + colour(p.shadow.color, 'shadow colour'));
        if (shadows.length) e.style.textShadow = shadows.join(', ');
        if (p.outline) {
          e.style.webkitTextStroke = px(2 * p.outline.px, 'outline') + ' ' + colour(p.outline.color, 'outline colour');
          e.style.paintOrder = 'stroke fill';
        }
        return e;
      },
      // lines lays the params' lines out in the box the way the worker measured them: one absolutely placed line per entry,
      // line_px apart, inside the padding, aligned; each word its own span (an inline block, so it can be scaled), the spaces
      // between them plain text. It returns [{el, words: [{el, text}]}].
      lines(parent) {
        const host = parent || root;
        const out = [];
        const pad = p.pad || 0;
        p.lines.forEach((line, i) => {
          const le = el('div', 'cf-line', host);
          ctx.typeset(le);
          le.style.left = px(pad, 'pad');
          le.style.width = px(item.box.w - 2 * pad, 'line width');
          le.style.top = px(pad + i * p.line_px, 'line top');
          le.style.height = px(p.line_px, 'line_px');
          le.style.textAlign = p.align === 'left' || p.align === 'right' ? p.align : 'center';
          const words = [];
          String(line).split(' ').forEach((w, j) => {
            if (j > 0) le.appendChild(document.createTextNode(' '));
            if (w === '') return;
            const we = el('span', 'cf-word', le);
            ctx.text(we, w);
            words.push({ el: we, text: w });
          });
          out.push({ el: le, words, index: i });
        });
        return out;
      },
    };
    return ctx;
  }

  const mounted = [];
  let stage = null;

  function bytesOf(b64) {
    const s = atob(b64);
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out.buffer;
  }

  async function mount(job, fonts) {
    problems.length = 0;
    seedRandom(job.seed);
    stage = document.getElementById('stage');
    stage.style.width = px(job.css_width, 'css_width');
    stage.style.height = px(job.css_height, 'css_height');
    // Fonts arrive as bytes: nothing is loaded by URL, and a face that fails to load fails the job.
    const loaded = new Set();
    for (const f of fonts) {
      try {
        const face = new FontFace(f.family, bytesOf(f.data), { display: 'block' });
        await face.load();
        document.fonts.add(face);
        if (face.status !== 'loaded') throw new Error('status ' + face.status);
        loaded.add(f.family);
      } catch (e) {
        report('font ' + f.family, e);
      }
    }
    for (const item of job.items) {
      const t = templates.get(item.template);
      if (!t) { report('item ' + item.id, new Error('unknown template ' + item.template)); continue; }
      const root = document.createElement('div');
      root.className = 'cf-item cf-' + item.template;
      root.dataset.item = item.id;
      root.style.left = px(item.box.x, 'box.x');
      root.style.top = px(item.box.y, 'box.y');
      root.style.width = px(item.box.w, 'box.w');
      root.style.height = px(item.box.h, 'box.h');
      stage.appendChild(root);
      const ticks = [];
      try {
        t.build(root, item.params, context(item, root, ticks, loaded));
      } catch (e) {
        report('item ' + item.id + ' (' + item.template + ')', e);
        continue;
      }
      const anims = root.getAnimations({ subtree: true });
      for (const a of anims) a.pause();
      root.hidden = true;
      mounted.push({ item, root, anims, ticks });
    }
    await document.fonts.ready;
    return { problems: problems.slice() };
  }

  // ---- 3. Seeking ------------------------------------------------------------------------------------------------------
  function seek(ms) {
    problems.length = 0;
    advance(ms);
    for (const m of mounted) {
      const on = ms >= m.item.start_ms && ms < m.item.end_ms;
      m.root.hidden = !on;
      if (!on) continue;
      const local = ms - m.item.start_ms;
      // Animations created by a tick are picked up and pinned too.
      for (const a of m.root.getAnimations({ subtree: true })) {
        if (a.playState !== 'paused') a.pause();
        a.currentTime = local;
      }
      for (const fn of m.ticks) {
        try { fn(local); } catch (e) { report('tick of ' + m.item.id, e); }
      }
    }
    // Anything else animated on the page stands at the same time.
    for (const a of document.getAnimations()) {
      if (a.playState !== 'paused') { a.pause(); a.currentTime = ms; }
    }
    void document.body.offsetHeight; // style and layout now, before the capture
    return { problems: problems.slice() };
  }

  window.__cfMotion = Object.freeze({ version: 1, register, mount, seek });
})();
