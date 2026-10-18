// counter v1: a number that counts up (or down) to its value and lands with a pop on its accent: "0 -> 12 REPS", "60 -> 100 KG".
// The digits are tabular and right-aligned in a slot as wide as the widest value, so the label next to them does not move
// while they count. The label (prefix and suffix) slides in after the count starts. The number is a function of time, set by a
// tick (textContent), so it is the same at every seek.
__cfMotion.register('counter', 1, (root, p, ctx) => {
  const m = p.motion;
  const c = p.count;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  const line = ctx.el('div', 'cf-line', wrap);
  ctx.typeset(line);
  const pad = p.pad || 0;
  line.style.left = ctx.px(pad, 'pad');
  line.style.width = ctx.px(ctx.box.w - 2 * pad, 'line width');
  line.style.top = ctx.px(pad, 'pad');
  line.style.height = ctx.px(p.line_px, 'line_px');
  line.style.textAlign = p.align === 'left' || p.align === 'right' ? p.align : 'center';
  const parts = [];
  const add = (s) => { if (s) { const e = ctx.el('span', 'cf-word', line); ctx.text(e, s); parts.push(e); return e; } return null; };
  const pre = add(c.prefix);
  const num = ctx.el('span', 'cf-word', line);
  num.style.fontVariantNumeric = 'tabular-nums';
  num.style.textAlign = 'right';
  num.style.minWidth = ctx.px(c.slot_px, 'slot_px');
  const suf = add(c.suffix);
  const fmt = (v) => v.toFixed(Math.max(0, Math.min(2, c.decimals | 0)));
  ctx.text(num, fmt(c.from));
  const start = p.enter_ms[0];
  const land = c.land_ms;
  const ease = (x) => 1 - Math.pow(1 - x, 3);
  ctx.tick((t) => {
    const x = land > start ? Math.min(1, Math.max(0, (t - start) / (land - start))) : 1;
    ctx.text(num, fmt(c.from + (c.to - c.from) * ease(x)));
  });
  ctx.anim(num, [
    { opacity: 0, transform: 'translateY(0px) scale(0.7)', filter: `blur(${m.blur_px}px)` },
    { opacity: 1, transform: 'translateY(0px) scale(1)', filter: 'blur(0px)' },
  ], start, m.dur_ms, 'cubic-bezier(0.16, 1, 0.3, 1)');
  // The landing: a pop on the accent.
  const a = ctx.anim(num, [
    { transform: 'scale(1)' },
    { transform: `scale(${m.overshoot + 0.06})`, offset: 0.35 },
    { transform: 'scale(1)' },
  ], land - 70, 240, 'ease-out', 'none');
  a.effect.composite = 'add';
  for (const h of p.hits_ms || []) {
    const b = ctx.anim(num, [
      { transform: 'scale(1)' },
      { transform: `scale(${m.overshoot})`, offset: 0.35 },
      { transform: 'scale(1)' },
    ], h - 70, 220, 'ease-out', 'none');
    b.effect.composite = 'add';
  }
  const labelAt = p.enter_ms.length > 1 ? p.enter_ms[1] : start + m.dur_ms * 0.4;
  for (const e of [pre, suf]) {
    if (!e) continue;
    ctx.anim(e, [
      { opacity: 0, transform: `translateY(${m.drift_px}px)`, filter: `blur(${m.blur_px * 0.6}px)` },
      { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
    ], labelAt, m.dur_ms, 'cubic-bezier(0.16, 1, 0.3, 1)');
  }
  ctx.anim(wrap, [
    { opacity: 1, transform: 'scale(1)', filter: 'blur(0px)' },
    { opacity: 0, transform: 'scale(0.9)', filter: `blur(${m.blur_px}px)` },
  ], p.exit_ms, p.exit_dur_ms, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
});
