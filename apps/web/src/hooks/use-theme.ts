import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

const KEY = "dashboard-theme";
const listeners = new Set<() => void>();

function stored(): Theme | null {
  const value = localStorage.getItem(KEY);
  return value === "light" || value === "dark" ? value : null;
}

function systemTheme(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

let current: Theme = stored() ?? systemTheme();

function publish(next: Theme) {
  current = next;
  document.documentElement.classList.toggle("dark", next === "dark");
  for (const listener of listeners) listener();
}

window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (event) => {
  if (!stored()) publish(event.matches ? "dark" : "light");
});

export function setTheme(next: Theme) {
  localStorage.setItem(KEY, next);
  publish(next);
}

export function toggleTheme() {
  setTheme(current === "dark" ? "light" : "dark");
}

/** Keep the pre-paint rule in sync with the inline script in `index.html`. */
export function useTheme(): Theme {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => current,
    () => "light",
  );
}
