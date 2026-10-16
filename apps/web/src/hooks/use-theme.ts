import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

const KEY = "dashboard-theme";
const DARK_QUERY = "(prefers-color-scheme: dark)";
const listeners = new Set<() => void>();

/**
 * Runs inline in <head> before first paint (see `routes/__root.tsx`) and puts the `dark` class on <html>.
 * The class is the source of truth for the hook below, so the two cannot disagree.
 * Keep it in sync with `KEY` and `DARK_QUERY`; storage may be blocked, so it is guarded.
 */
export const THEME_SCRIPT = `(function(){try{var s=null;try{s=localStorage.getItem("${KEY}")}catch(e){}var d=s?s==="dark":window.matchMedia("${DARK_QUERY}").matches;document.documentElement.classList.toggle("dark",d)}catch(e){}})();`;

function stored(): Theme | null {
  try {
    const value = localStorage.getItem(KEY);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}

function current(): Theme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

function publish(next: Theme) {
  document.documentElement.classList.toggle("dark", next === "dark");
  for (const listener of listeners) listener();
}

function onSystemChange(event: MediaQueryListEvent) {
  if (!stored()) publish(event.matches ? "dark" : "light");
}

/** Follows the system theme while the user has made no choice. Only ever called in the browser. */
function subscribe(listener: () => void) {
  if (listeners.size === 0) window.matchMedia(DARK_QUERY).addEventListener("change", onSystemChange);
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) window.matchMedia(DARK_QUERY).removeEventListener("change", onSystemChange);
  };
}

export function setTheme(next: Theme) {
  try {
    localStorage.setItem(KEY, next);
  } catch {
    // Storage is blocked: the choice lasts for this page only.
  }
  publish(next);
}

export function toggleTheme() {
  setTheme(current() === "dark" ? "light" : "dark");
}

/** The server cannot know the theme; during hydration this returns "light", then re-renders with the real one. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, current, () => "light");
}
