import * as React from "react";

/** How soon presigned URLs (pictures, videos) may be fetched again because one would not load. */
export const URL_REFRESH_MIN_MS = 10_000;

/**
 * A guard for "fetch again because a URL expired": `refresh()` runs the callback at most once per `minMs`, however many
 * pictures or videos fail together. `reset()` lets the next call through (a refresh that was planned, not a failure).
 */
export function useRefreshThrottle(callback: () => void, minMs: number = URL_REFRESH_MIN_MS) {
  const last = React.useRef(0);
  const latest = React.useRef(callback);
  latest.current = callback;
  const refresh = React.useCallback(() => {
    const now = Date.now();
    if (now - last.current < minMs) return;
    last.current = now;
    latest.current();
  }, [minMs]);
  const reset = React.useCallback(() => {
    last.current = 0;
  }, []);
  return { refresh, reset };
}
