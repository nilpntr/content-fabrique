// blur_in v1: the title sharpens out of a blur while it drifts into place, line by line. Quieter than title_pop: no overshoot,
// a long ease-out. It leaves the way it came, drifting up into a blur.
__cfMotion.register('blur_in', 1, (root, p, ctx) => {
  const m = p.motion;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  const lines = ctx.lines(wrap);
  lines.forEach((l, i) => {
    const t0 = p.enter_ms[Math.min(i, p.enter_ms.length - 1)];
    ctx.anim(l.el, [
      { opacity: 0, transform: `translateY(${m.drift_px}px) scale(1.03)`, filter: `blur(${2 * m.blur_px}px)` },
      { opacity: 1, transform: 'translateY(0px) scale(1)', filter: 'blur(0px)' },
    ], t0, m.dur_ms * 1.6, 'cubic-bezier(0.16, 1, 0.3, 1)');
  });
  for (const h of p.hits_ms || []) {
    const a = ctx.anim(wrap, [
      { filter: 'blur(0px)', opacity: 1 },
      { filter: `blur(${m.blur_px * 0.25}px)`, opacity: 0.85, offset: 0.3 },
      { filter: 'blur(0px)', opacity: 1 },
    ], h - 50, 220, 'ease-out', 'none');
    a.effect.composite = 'replace';
  }
  ctx.anim(wrap, [
    { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
    { opacity: 0, transform: `translateY(${-m.drift_px}px)`, filter: `blur(${2 * m.blur_px}px)` },
  ], p.exit_ms, p.exit_dur_ms, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
});
