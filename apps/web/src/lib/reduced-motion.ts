const QUERY = "(prefers-reduced-motion: reduce)";

let cached: { source: typeof window.matchMedia; list: MediaQueryList } | undefined;

/**
 * The one MediaQueryList for prefers-reduced-motion, made on first use and shared by everything that asks (the glider,
 * the library cards). Rebuilt only if `window.matchMedia` itself was replaced, which tests do.
 */
export function reducedMotionQuery(): MediaQueryList | undefined {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return undefined;
  if (cached?.source !== window.matchMedia) cached = { source: window.matchMedia, list: window.matchMedia(QUERY) };
  return cached.list;
}

export function prefersReducedMotion(): boolean {
  return reducedMotionQuery()?.matches === true;
}
