// outro_card v1: the loop card at the end ("PART 2 TOMORROW", "SAVE FOR BACK DAY"). The letters drop in one after another
// with a blur, an accent line grows out from the centre under the text, the card breathes while it holds, and it leaves with a
// quick push toward the viewer and a blur, timed to end on the last frame so the video loops back to its hook.
__cfMotion.register('outro_card', 1, (root, p, ctx) => {
  const m = p.motion;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  wrap.style.transformOrigin = '50% 50%';
  const lines = ctx.lines(wrap);
  // Letters: each word is split into letter spans (textContent per letter).
  const letters = [];
  for (const l of lines) {
    for (const w of l.words) {
      const chars = Array.from(w.text);
      w.el.textContent = '';
      for (const ch of chars) {
        const s = document.createElement('span');
        s.style.display = 'inline-block';
        s.textContent = ch;
        w.el.appendChild(s);
        letters.push({ el: s, line: l.index });
      }
    }
  }
  const per = Math.max(12, Math.min(45, p.stagger_ms / 3));
  letters.forEach((c, i) => {
    const t0 = p.enter_ms[Math.min(c.line, p.enter_ms.length - 1)] + i * per;
    ctx.anim(c.el, [
      { opacity: 0, transform: `translateY(${-0.22 * p.font_px}px)`, filter: `blur(${m.blur_px}px)` },
      { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
    ], t0, m.dur_ms, 'cubic-bezier(0.16, 1, 0.3, 1)');
  });
  const lineH = Math.max(3, 0.06 * p.font_px);
  const rule = ctx.el('div', 'cf-clip');
  rule.style.left = ctx.px(p.pad + 0.1 * (ctx.box.w - 2 * p.pad), 'rule left');
  rule.style.width = ctx.px(0.8 * (ctx.box.w - 2 * p.pad), 'rule width');
  const fm = ctx.metrics();
  rule.style.top = ctx.px(p.pad + (lines.length - 1) * p.line_px + fm.baseline + 0.16 * fm.cap, 'rule top');
  rule.style.height = ctx.px(lineH, 'rule height');
  rule.style.background = ctx.colour(p.accent, 'accent');
  const ruleAt = p.enter_ms[0] + letters.length * per;
  ctx.anim(rule, [{ transform: 'scaleX(0)' }, { transform: 'scaleX(1)' }], ruleAt, m.dur_ms, 'cubic-bezier(0.65, 0, 0.35, 1)');
  const settled = ruleAt + m.dur_ms;
  if (m.float_px > 0 && p.exit_ms > settled) {
    const a = ctx.anim(wrap, [{ transform: 'scale(1)' }, { transform: 'scale(1.012)' }, { transform: 'scale(1)' }], settled, 1600, 'ease-in-out', 'none');
    a.effect.updateTiming({ iterations: Math.max(1, Math.ceil((p.exit_ms - settled) / 1600)) });
    a.effect.composite = 'add';
  }
  for (const h of p.hits_ms || []) {
    const a = ctx.anim(rule, [{ transform: 'scaleY(1)' }, { transform: 'scaleY(2.2)', offset: 0.35 }, { transform: 'scaleY(1)' }], h - 70, 220, 'ease-out', 'none');
    a.effect.composite = 'add';
  }
  ctx.anim(wrap, [
    { opacity: 1, transform: 'scale(1)', filter: 'blur(0px)' },
    { opacity: 0, transform: 'scale(1.05)', filter: `blur(${1.5 * m.blur_px}px)` },
  ], p.exit_ms, p.exit_dur_ms, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
});
