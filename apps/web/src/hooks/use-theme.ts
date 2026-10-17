import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

const KEY = "cf-theme";
/** The key before the rename to Content Fabrique. Its value moves to `KEY` once, so users keep their theme. */
const LEGACY_KEY = "dashboard-theme";
const DARK_QUERY = "(prefers-color-scheme: dark)";
const listeners = new Set<() => void>();
// Set by `setTheme`. With storage blocked nothing persists, so this alone says the user has chosen.
let userChose = false;

/**
 * Runs inline in <head> before first paint (see `routes/__root.tsx`) and puts the `dark` class on <html>.
 * The class is the source of truth for the hook below, so the two cannot disagree.
 * It also does the one-time move from `LEGACY_KEY` to `KEY`; it runs on every page load, before the hook reads storage.
 * Keep it in sync with `KEY`, `LEGACY_KEY` and `DARK_QUERY`; storage may be blocked, so it is guarded.
 * Only "light" and "dark" are accepted, from either key.
 */
export const THEME_SCRIPT = `(function(){try{var s=null;try{s=localStorage.getItem("${KEY}");if(s===null){var o=localStorage.getItem("${LEGACY_KEY}");if(o==="dark"||o==="light"){s=o;localStorage.setItem("${KEY}",o);localStorage.removeItem("${LEGACY_KEY}")}}}catch(e){}var d=s==="dark"||s==="light"?s==="dark":window.matchMedia("${DARK_QUERY}").matches;document.documentElement.classList.toggle("dark",d)}catch(e){}})();`;

function valid(value: string | null): Theme | null {
  return value === "light" || value === "dark" ? value : null;
}

/** The saved choice. Falls back to `LEGACY_KEY` and moves it to `KEY` when it can; if it cannot, the old value still counts. */
function stored(): Theme | null {
  try {
    const value = valid(localStorage.getItem(KEY));
    if (value) return value;
    const legacy = valid(localStorage.getItem(LEGACY_KEY));
    if (!legacy) return null;
    try {
      localStorage.setItem(KEY, legacy);
      localStorage.removeItem(LEGACY_KEY);
    } catch {
      // Keep the legacy value: it is still the user's choice.
    }
    return legacy;
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
  if (!userChose && !stored()) publish(event.matches ? "dark" : "light");
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
  userChose = true;
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
