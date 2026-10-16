export function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}

export function motion(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function ms(name: string, fallback: number): number {
  const value = Number.parseFloat(motion(name));
  return Number.isFinite(value) ? value : fallback;
}

/** Layout-space offset of `el` inside a positioned `container`; transforms and scrolling do not count. */
function offsetWithin(el: HTMLElement, container: HTMLElement) {
  let x = 0;
  let y = 0;
  let node: HTMLElement | null = el;
  while (node && node !== container) {
    x += node.offsetLeft;
    y += node.offsetTop;
    const parent = node.offsetParent as HTMLElement | null;
    if (parent && parent !== container) {
      x += parent.clientLeft;
      y += parent.clientTop;
    }
    node = parent;
  }
  return { x, y };
}

export class Glider {
  private target: HTMLElement | null = null;
  private shown = false;
  private hideTimer = 0;
  private size = "";
  private readonly resize?: ResizeObserver;

  constructor(
    private readonly el: HTMLElement,
    private readonly container: HTMLElement,
  ) {
    // Observing delivers an initial entry; only a real size change should snap the glider along.
    if (typeof ResizeObserver !== "undefined") {
      this.resize = new ResizeObserver(() => {
        if (this.target && this.sizeOf(this.target) !== this.size) this.place(true);
      });
    }
  }

  to(target: HTMLElement | null, linger = 0) {
    window.clearTimeout(this.hideTimer);
    if (!target) {
      if (!this.shown) return;
      if (linger > 0) this.hideTimer = window.setTimeout(() => this.hide(), linger);
      else this.hide();
      return;
    }
    if (target !== this.target) {
      if (this.target) this.resize?.unobserve(this.target);
      this.target = target;
      this.resize?.observe(target);
    }
    this.place(!this.shown);
    this.shown = true;
    this.el.dataset.state = "shown";
    if (target.dataset.variant) this.el.dataset.variant = target.dataset.variant;
    else delete this.el.dataset.variant;
  }

  destroy() {
    window.clearTimeout(this.hideTimer);
    this.resize?.disconnect();
  }

  private hide() {
    this.shown = false;
    this.el.dataset.state = "hidden";
    if (this.target) this.resize?.unobserve(this.target);
    this.target = null;
  }

  private sizeOf(target: HTMLElement) {
    return `${target.offsetWidth}x${target.offsetHeight}`;
  }

  private place(snap: boolean) {
    const target = this.target;
    if (!target?.isConnected) return;
    const { x, y } = offsetWithin(target, this.container);
    const width = target.offsetWidth;
    const height = target.offsetHeight;
    this.size = `${width}x${height}`;
    const style = this.el.style;
    if (snap) style.transitionProperty = "opacity";
    style.translate = `${x}px ${y}px`;
    style.width = `${width}px`;
    style.height = `${height}px`;
    if (snap) {
      void getComputedStyle(this.el).translate;
      style.transitionProperty = "";
    }
  }
}

export function glideToHighlighted(list: HTMLElement, glider: Glider): () => void {
  const sync = () => glider.to(list.querySelector<HTMLElement>("[data-highlighted]"));
  const observer = new MutationObserver(sync);
  observer.observe(list, { subtree: true, childList: true, attributes: true, attributeFilter: ["data-highlighted"] });
  sync();
  return () => {
    observer.disconnect();
    glider.destroy();
  };
}

export function handOff(prev: HTMLElement, next: HTMLElement, icon: Element | null) {
  const from = prev.getBoundingClientRect();
  for (const animation of prev.getAnimations?.() ?? []) animation.cancel();
  if (typeof next.animate !== "function" || prefersReducedMotion() || from.width === 0) return;
  for (const animation of next.getAnimations()) animation.cancel();
  const to = next.getBoundingClientRect();
  const dx = from.left - to.left;
  const dy = from.top - to.top;
  if (Math.abs(dx) < 1 && Math.abs(dy) < 1) return;

  const duration = ms("--dur-spring", 640);
  const easing = motion("--motion-spring") || "cubic-bezier(0.22, 1, 0.36, 1)";
  const sx = from.width / to.width;
  const sy = from.height / to.height;
  const originY = dy < 0 ? 0 : to.height;
  const stretch = Math.min(0.16, (Math.abs(dy) / to.height) * 0.07);
  const curve = "cubic-bezier(0.4, 0, 0.4, 1)";

  next.style.transformOrigin = `50% ${originY}px`;
  next.animate(
    [{ translate: `${dx - (to.width / 2) * (1 - sx)}px ${dy - originY * (1 - sy)}px` }, { translate: "0 0" }],
    { duration, easing },
  );
  next.animate(
    [
      { scale: `${sx} ${sy}`, easing: curve },
      { scale: `1 ${1 + stretch}`, offset: 0.11, easing: curve },
      { scale: "1 1", offset: 0.26, easing: curve },
      { scale: `1 ${1 - stretch * 0.45}`, offset: 0.36, easing: curve },
      { scale: "1 1", offset: 0.6 },
    ],
    { duration },
  );
  icon?.animate(
    [
      { scale: 1, easing: curve },
      { scale: 1.16, offset: 0.34, easing: curve },
      { scale: 0.96, offset: 0.68, easing: curve },
      { scale: 1 },
    ],
    { duration: 460, delay: duration * 0.1 },
  );
  next.animate([{ translate: "-100% 0" }, { translate: "100% 0" }], {
    pseudoElement: "::after",
    duration: 540,
    delay: duration * 0.26,
    easing: "cubic-bezier(0.45, 0, 0.25, 1)",
  });
}
