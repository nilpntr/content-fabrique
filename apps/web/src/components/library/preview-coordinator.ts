import { useSyncExternalStore } from "react";

/**
 * Which card is previewing. Only one at a time: a card that starts previewing takes the slot and the previous
 * holder's video unmounts. Module state on purpose, so it also holds across two grids on one page.
 */
let activeId: string | null = null;
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function claimPreview(id: string) {
  if (activeId === id) return;
  activeId = id;
  emit();
}

export function releasePreview(id: string) {
  if (activeId !== id) return;
  activeId = null;
  emit();
}

export function useHoldsPreview(id: string): boolean {
  return useSyncExternalStore(
    subscribe,
    () => activeId === id,
    () => false,
  );
}
