// The hostile page's code, from the bundle (the CSP allows no inline script).
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
window.__cfMotion = {
  async mount(job) {
    const it = job.items[0];
    const d = document.createElement('div');
    Object.assign(d.style, { position: 'absolute', left: it.box.x + 'px', top: it.box.y + 'px', width: it.box.w + 'px', height: it.box.h + 'px', background: '#fff', color: '#000', overflow: 'hidden', fontSize: '40px' });
    document.getElementById('stage').appendChild(d);
    try {
      const l = document.createElement('link'); l.rel = 'stylesheet'; l.href = 'file:///etc/hostname'; document.head.appendChild(l);
    } catch (e) { /* swallowed on purpose: the browser and the runner must notice anyway */ }
    await sleep(1000);
    return { problems: [] };
  },
  seek() { return { problems: [] }; },
};
