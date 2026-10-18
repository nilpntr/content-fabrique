// title_pop v1: the title arrives word by word. Each word scales up from small with a blur, overshoots a little and settles,
// its peak landing on its accent (the worker places the starts so that the overshoot peak is on the beat). After the entrance
// the title floats gently (lively, hype) and pulses on the accents that are left; the exit takes the words away last first,
// smaller and blurred.
__cfMotion.register('title_pop', 1, (root, p, ctx) => {
  const m = p.motion;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  wrap.style.transformOrigin = '50% 55%';
  const words = ctx.lines(wrap).flatMap((l) => l.words);
  const lift = 0.18 * p.font_px;
  let landed = 0;
  words.forEach((w, i) => {
    const t0 = p.enter_ms[Math.min(i, p.enter_ms.length - 1)];
    landed = Math.max(landed, t0 + m.dur_ms);
    ctx.anim(w.el, [
      { opacity: 0, transform: `translateY(${lift}px) scale(0.55)`, filter: `blur(${m.blur_px}px)`, easing: 'cubic-bezier(0.2, 0.9, 0.3, 1)' },
      { opacity: 1, transform: `translateY(0px) scale(${m.overshoot})`, filter: 'blur(0px)', offset: 0.6, easing: 'cubic-bezier(0.4, 0, 0.4, 1)' },
      { opacity: 1, transform: 'translateY(0px) scale(1)', filter: 'blur(0px)' },
    ], t0, m.dur_ms);
  });
  // Accent hits after the entrance: a short pulse of the whole title, peaking on the accent.
  for (const h of p.hits_ms || []) {
    const a = ctx.anim(wrap, [
      { transform: 'scale(1)' },
      { transform: `scale(${1 + (m.overshoot - 1) * 0.5})`, offset: 0.35 },
      { transform: 'scale(1)' },
    ], h - 70, 200, 'ease-out', 'none');
    a.effect.composite = 'add';
  }
  if (m.float_px > 0 && p.exit_ms > landed) {
    const a = ctx.anim(wrap, [
      { transform: 'translateY(0px)' },
      { transform: `translateY(${-m.float_px}px)` },
      { transform: 'translateY(0px)' },
    ], landed, 1800, 'ease-in-out', 'none');
    a.effect.updateTiming({ iterations: Math.max(1, Math.ceil((p.exit_ms - landed) / 1800)) });
    a.effect.composite = 'add';
  }
  // The exit: the words leave last first, the last one gone when the item ends.
  const d = p.exit_dur_ms * 0.6;
  const step = words.length > 1 ? Math.min(40, (p.exit_dur_ms - d) / (words.length - 1)) : 0;
  words.slice().reverse().forEach((w, k) => {
    ctx.anim(w.el, [
      { opacity: 1, transform: 'translateY(0px) scale(1)', filter: 'blur(0px)' },
      { opacity: 0, transform: `translateY(${-lift * 0.6}px) scale(0.85)`, filter: `blur(${m.blur_px * 0.6}px)` },
    ], p.exit_ms + k * step, d, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  });
});
