/** Time and chance, injectable so that the tests control them. */
export interface Clock {
  now(): number;
  /** Resolves after `ms`, or rejects with the signal's reason when it aborts first. */
  sleep(ms: number, signal?: AbortSignal): Promise<void>;
  /** In [0, 1). */
  random(): number;
}

export const realClock: Clock = {
  now: () => Date.now(),
  random: () => Math.random(),
  sleep(ms, signal) {
    return new Promise<void>((resolve, reject) => {
      if (signal?.aborted) return reject(signal.reason);
      const onAbort = () => {
        clearTimeout(timer);
        reject(signal?.reason);
      };
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      }, ms);
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  },
};

/**
 * Exponential backoff with jitter: the delay before retry number `attempt` (0 for the first retry) is half of
 * `min(cap, base * 2^attempt)` plus a random share of the other half, so the delays grow but many uploads that failed
 * together do not all come back at the same moment.
 */
export function backoffMs(attempt: number, random: number, base = 500, cap = 30_000): number {
  const ceiling = Math.min(cap, base * 2 ** Math.min(attempt, 30));
  return Math.round(ceiling / 2 + (random * ceiling) / 2);
}

/** Whether the browser thinks it is online, and a way to wait until it is. A lost connection is not the part's fault. */
export interface Connectivity {
  isOnline(): boolean;
  waitOnline(signal: AbortSignal): Promise<void>;
}

export const browserConnectivity: Connectivity = {
  isOnline: () => typeof navigator === "undefined" || navigator.onLine !== false,
  waitOnline(signal) {
    return new Promise<void>((resolve, reject) => {
      if (typeof window === "undefined" || typeof navigator === "undefined" || navigator.onLine !== false) return resolve();
      if (signal.aborted) return reject(signal.reason);
      const done = () => {
        window.removeEventListener("online", onOnline);
        signal.removeEventListener("abort", onAbort);
      };
      const onOnline = () => {
        done();
        resolve();
      };
      const onAbort = () => {
        done();
        reject(signal.reason);
      };
      window.addEventListener("online", onOnline);
      signal.addEventListener("abort", onAbort, { once: true });
    });
  },
};
