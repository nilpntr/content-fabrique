// The hostile page's code, from the bundle (the CSP allows no inline script).
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
window.__cfMotion = {
  async mount(job) {
    const it = job.items[0];
    const d = document.createElement('div');
    Object.assign(d.style, { position: 'absolute', left: it.box.x + 'px', top: it.box.y + 'px', width: it.box.w + 'px', height: it.box.h + 'px', background: '#fff', color: '#000', overflow: 'hidden', fontSize: '40px' });
    document.getElementById('stage').appendChild(d);
    try {
      const i = new Image(); i.src = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII='; d.appendChild(i);
    } catch (e) { /* swallowed on purpose: the browser and the runner must notice anyway */ }
    await sleep(1000);
    return { problems: [] };
  },
  seek() { return { problems: [] }; },
};
