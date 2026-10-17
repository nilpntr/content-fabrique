import { useSyncExternalStore } from "react";
import { prefersReducedMotion, reducedMotionQuery } from "@/lib/reduced-motion";

function subscribe(onChange: () => void) {
  const list = reducedMotionQuery();
  list?.addEventListener?.("change", onChange);
  return () => list?.removeEventListener?.("change", onChange);
}

/** True when the user asked the system for less motion. Server render and tests without matchMedia answer false. */
export function usePrefersReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, prefersReducedMotion, () => false);
}
