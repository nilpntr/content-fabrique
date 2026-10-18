// lower_label v1: a small label on a plate, for a name, an exercise or a tag. An accent stripe drops in at the left, the plate
// wipes out from it, and the words rise into the plate from below its edge. It leaves the other way: the words sink, the plate
// wipes back into the stripe, the stripe goes.
__cfMotion.register('lower_label', 1, (root, p, ctx) => {
  const m = p.motion;
  const plate = ctx.el('div', 'cf-clip');
  plate.style.inset = '0';
  plate.style.background = ctx.colour(p.plate || 'rgba(0,0,0,0.8)', 'plate');
  plate.style.transformOrigin = '0% 50%';
  const stripeW = Math.max(3, 0.14 * p.font_px);
  const stripe = ctx.el('div', 'cf-clip');
  stripe.style.left = '0px';
  stripe.style.top = '0px';
  stripe.style.bottom = '0px';
  stripe.style.width = ctx.px(stripeW, 'stripe');
  stripe.style.background = ctx.colour(p.accent, 'accent');
  stripe.style.transformOrigin = '50% 0%';
  const body = ctx.el('div', 'cf-clip');
  body.style.inset = '0';
  body.style.clipPath = 'inset(0px)';
  const lines = ctx.lines(body);
  const t0 = p.enter_ms[0];
  ctx.anim(stripe, [{ transform: 'scaleY(0)' }, { transform: 'scaleY(1)' }], t0, m.dur_ms * 0.5, 'cubic-bezier(0.16, 1, 0.3, 1)');
  ctx.anim(plate, [{ transform: 'scaleX(0)' }, { transform: 'scaleX(1)' }], t0 + m.dur_ms * 0.25, m.dur_ms, 'cubic-bezier(0.16, 1, 0.3, 1)');
  const textAt = p.enter_ms.length > 1 ? p.enter_ms[1] : t0 + m.dur_ms * 0.6;
  lines.forEach((l, i) => {
    ctx.anim(l.el, [
      { transform: `translateY(${p.line_px}px)`, opacity: 0 },
      { transform: 'translateY(0px)', opacity: 1 },
    ], textAt + i * p.stagger_ms, m.dur_ms, 'cubic-bezier(0.16, 1, 0.3, 1)');
  });
  for (const h of p.hits_ms || []) {
    const a = ctx.anim(stripe, [
      { transform: 'scaleX(1)' }, { transform: 'scaleX(1.8)', offset: 0.35 }, { transform: 'scaleX(1)' },
    ], h - 70, 220, 'ease-out', 'none');
    a.effect.composite = 'add';
  }
  const x = p.exit_ms;
  const d = p.exit_dur_ms;
  lines.forEach((l) => {
    ctx.anim(l.el, [{ transform: 'translateY(0px)', opacity: 1 }, { transform: `translateY(${p.line_px}px)`, opacity: 0 }], x, d * 0.5, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  });
  ctx.anim(plate, [{ transform: 'scaleX(1)' }, { transform: 'scaleX(0)' }], x + d * 0.3, d * 0.5, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  ctx.anim(stripe, [{ transform: 'scaleY(1)', opacity: 1 }, { transform: 'scaleY(0)', opacity: 0 }], x + d * 0.7, d * 0.3, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
});
