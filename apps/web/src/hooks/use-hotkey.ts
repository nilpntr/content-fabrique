import { useEffect, useRef, useSyncExternalStore } from "react";

function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
}

export function useHotkey(key: string, handler: (event: KeyboardEvent) => void, { mod = false } = {}) {
  const latest = useRef(handler);
  latest.current = handler;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== key.toLowerCase()) return;
      const withMod = event.metaKey || event.ctrlKey;
      if (mod !== withMod || event.altKey) return;
      if (!mod && typing(event.target)) return;
      event.preventDefault();
      latest.current(event);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [key, mod]);
}

const noSubscription = () => () => {};

function platformModKey(): "⌘" | "Ctrl" {
  return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? "⌘" : "Ctrl";
}

/** "⌘" on Apple platforms, "Ctrl" elsewhere. The server renders "Ctrl", so hydration matches and Apple browsers switch after. */
export function useModKey(): "⌘" | "Ctrl" {
  return useSyncExternalStore(noSubscription, platformModKey, () => "Ctrl");
}
