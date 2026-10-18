// highlight_word v1: the line rises in, then one word gets a marker: a slanted bar in the accent colour swipes in behind it
// from the left, and the word turns to the colour that reads on the marker. The marker swipes on its accent. The exit wipes the
// marker back out and fades the line down.
__cfMotion.register('highlight_word', 1, (root, p, ctx) => {
  const m = p.motion;
  const wrap = ctx.el('div', 'cf-clip');
  wrap.style.inset = '0';
  const lines = ctx.lines(wrap);
  lines.forEach((l, i) => {
    const t0 = p.enter_ms[Math.min(i, p.enter_ms.length - 1)];
    ctx.anim(l.el, [
      { opacity: 0, transform: `translateY(${m.drift_px}px)`, filter: `blur(${m.blur_px}px)` },
      { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
    ], t0, m.dur_ms * 1.2, 'cubic-bezier(0.16, 1, 0.3, 1)');
  });
  const h = p.highlight || { line: 0, word: 0 };
  const line = lines[Math.min(Math.max(0, h.line | 0), lines.length - 1)];
  const word = line.words[Math.min(Math.max(0, h.word | 0), line.words.length - 1)];
  if (word) {
    // The marker sits behind the word, in the line's own box: from just under the cap line to just under the baseline.
    const marker = document.createElement('div');
    line.el.insertBefore(marker, line.el.firstChild);
    marker.style.position = 'absolute';
    marker.style.zIndex = '-1';
    const padX = 0.06 * p.font_px;
    marker.style.left = ctx.px(word.el.offsetLeft - padX, 'marker left');
    marker.style.width = ctx.px(word.el.offsetWidth + 2 * padX, 'marker width');
    const fm = ctx.metrics();
    marker.style.top = ctx.px(fm.baseline - 0.62 * fm.cap, 'marker top');
    marker.style.height = ctx.px(0.74 * fm.cap, 'marker height');
    marker.style.background = ctx.colour(p.accent, 'accent');
    marker.style.transformOrigin = '0% 50%';
    line.el.style.isolation = 'isolate';
    const swipe = p.swipe_ms;
    ctx.anim(marker, [
      { transform: 'skewX(-8deg) scaleX(0)' },
      { transform: 'skewX(-8deg) scaleX(1)' },
    ], swipe, m.dur_ms, 'cubic-bezier(0.65, 0, 0.35, 1)');
    ctx.anim(word.el, [
      { color: ctx.colour(p.fill, 'fill') },
      { color: ctx.colour(p.on_accent, 'on_accent') },
    ], swipe + m.dur_ms * 0.35, m.dur_ms * 0.3, 'linear');
    for (const t of p.hits_ms || []) {
      const a = ctx.anim(word.el, [
        { transform: 'scale(1)' },
        { transform: `scale(${m.overshoot})`, offset: 0.35 },
        { transform: 'scale(1)' },
      ], t - 70, 220, 'ease-out', 'none');
      a.effect.composite = 'add';
    }
    ctx.anim(marker, [
      { transform: 'skewX(-8deg) scaleX(1)', transformOrigin: '100% 50%' },
      { transform: 'skewX(-8deg) scaleX(0)', transformOrigin: '100% 50%' },
    ], p.exit_ms, p.exit_dur_ms * 0.6, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
  }
  ctx.anim(wrap, [
    { opacity: 1, transform: 'translateY(0px)', filter: 'blur(0px)' },
    { opacity: 0, transform: `translateY(${m.drift_px * 0.6}px)`, filter: `blur(${m.blur_px}px)` },
  ], p.exit_ms + p.exit_dur_ms * 0.3, p.exit_dur_ms * 0.7, 'cubic-bezier(0.7, 0, 0.84, 0)', 'forwards');
});
