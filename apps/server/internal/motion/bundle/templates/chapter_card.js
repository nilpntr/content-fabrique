// chapter_card v1: a numbered chapter title ("1. SHRUGS VOOR UPPER TRAPS"). The number pops first in the accent colour (a
// small turn and an overshoot), then the rest of the line sweeps in after it, word by word from the left, each wiped in from
// behind a hard edge with a short blur. The two-line variant puts the number on a line of its own. It leaves by sliding down
// and blurring out, the number last.
__cfMotion.register('chapter_card', 1, (root, p, ctx) => {
  const m = p.motion;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  const lines = ctx.lines(wrap);
  const n = Math.max(1, p.number_words | 0);
  const number = lines[0].words.slice(0, n);
  const rest = lines[0].words.slice(n).concat(...lines.slice(1).map((l) => l.words));
  for (const w of number) {
    w.el.style.color = ctx.colour(p.accent, 'accent');
    ctx.anim(w.el, [
      { opacity: 0, transform: 'scale(0.3) rotate(-10deg)', filter: `blur(${m.blur_px}px)`, easing: 'cubic-bezier(0.2, 0.9, 0.3, 1)' },
      { opacity: 1, transform: `scale(${m.overshoot + 0.08}) rotate(2deg)`, filter: 'blur(0px)', offset: 0.55, easing: 'cubic-bezier(0.4, 0, 0.4, 1)' },
      { opacity: 1, transform: 'scale(1) rotate(0deg)', filter: 'blur(0px)' },
    ], p.enter_ms[0], m.dur_ms);
  }
  const sweep = p.enter_ms.length > 1 ? p.enter_ms[1] : p.enter_ms[0] + m.dur_ms * 0.5;
  const step = Math.max(25, Math.min(70, p.stagger_ms / 2));
  rest.forEach((w, k) => {
    ctx.anim(w.el, [
      { opacity: 0, transform: `translateX(${-0.45 * p.font_px}px)`, clipPath: 'inset(-40% 100% -40% 0%)', filter: `blur(${m.blur_px * 0.7}px)` },
      { opacity: 1, transform: 'translateX(0px)', clipPath: 'inset(-40% -10% -40% 0%)', filter: 'blur(0px)' },
    ], sweep + k * step, m.dur_ms * 1.1, 'cubic-bezier(0.16, 1, 0.3, 1)');
  });
  for (const h of p.hits_ms || []) {
    for (const w of number) {
      const a = ctx.anim(w.el, [
        { transform: 'scale(1)' },
        { transform: `scale(${m.overshoot + 0.06})`, offset: 0.35 },
        { transform: 'scale(1)' },
      ], h - 70, 220, 'ease-out', 'none');
      a.effect.composite = 'add';
    }
  }
  const drop = 0.22 * p.font_px;
  rest.slice().reverse().forEach((w, k) => {
    ctx.anim(w.el, [
      { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
      { opacity: 0, transform: `translateY(${drop}px)`, filter: `blur(${m.blur_px * 0.6}px)` },
    ], p.exit_ms + k * Math.min(25, p.exit_dur_ms * 0.4 / Math.max(1, rest.length)), p.exit_dur_ms * 0.6, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  });
  for (const w of number) {
    ctx.anim(w.el, [
      { opacity: 1, transform: 'scale(1)', filter: 'blur(0px)' },
      { opacity: 0, transform: 'scale(0.6)', filter: `blur(${m.blur_px * 0.6}px)` },
    ], p.exit_ms + p.exit_dur_ms * 0.4, p.exit_dur_ms * 0.6, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  }
});
