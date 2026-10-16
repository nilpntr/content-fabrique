import { act, render } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, expect, test, vi } from "vitest";
import { THEME_SCRIPT, setTheme, toggleTheme, useTheme } from "./use-theme";

function Probe() {
  return <span>{useTheme()}</span>;
}

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});

test("server render returns the light snapshot without reading the DOM", () => {
  document.documentElement.classList.add("dark");
  expect(renderToString(<Probe />)).toBe("<span>light</span>");
});

test("the head script sets the dark class from storage, else from the system", () => {
  localStorage.setItem("dashboard-theme", "dark");
  new Function(THEME_SCRIPT)();
  expect(document.documentElement.classList.contains("dark")).toBe(true);

  localStorage.setItem("dashboard-theme", "light");
  new Function(THEME_SCRIPT)();
  expect(document.documentElement.classList.contains("dark")).toBe(false);
});

test("the head script survives blocked storage", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new DOMException("blocked", "SecurityError");
  });
  expect(() => new Function(THEME_SCRIPT)()).not.toThrow();
});

test("toggling still works when storage is blocked", () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("blocked", "QuotaExceededError");
  });
  expect(() => toggleTheme()).not.toThrow();
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  setTheme("light");
  expect(document.documentElement.classList.contains("dark")).toBe(false);
});

test("the head script ignores an invalid stored value and follows the system", () => {
  const matchMedia = vi.spyOn(window, "matchMedia").mockReturnValue({ matches: false } as MediaQueryList);
  localStorage.setItem("dashboard-theme", "purple");
  new Function(THEME_SCRIPT)();
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  matchMedia.mockReturnValue({ matches: true } as MediaQueryList);
  new Function(THEME_SCRIPT)();
  expect(document.documentElement.classList.contains("dark")).toBe(true);
});

// `setTheme` sets a module-level flag, so these load a fresh module.
async function mountWithSystemEvents() {
  vi.resetModules();
  let onChange: ((event: MediaQueryListEvent) => void) | undefined;
  vi.spyOn(window, "matchMedia").mockReturnValue({
    matches: false,
    addEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => (onChange = listener),
    removeEventListener: () => {},
  } as unknown as MediaQueryList);
  const theme = await import("./use-theme");
  function Subscriber() {
    return <span>{theme.useTheme()}</span>;
  }
  const view = render(<Subscriber />);
  return { theme, view, systemChange: (dark: boolean) => act(() => onChange?.({ matches: dark } as MediaQueryListEvent)) };
}

test("the theme follows the system until the user chooses", async () => {
  const { view, systemChange } = await mountWithSystemEvents();
  systemChange(true);
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  systemChange(false);
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  view.unmount();
});

test("with storage blocked, an in-session choice survives a system theme change", async () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("blocked", "QuotaExceededError");
  });
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new DOMException("blocked", "SecurityError");
  });
  const { theme, view, systemChange } = await mountWithSystemEvents();
  act(() => theme.setTheme("light"));
  systemChange(true);
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  act(() => theme.setTheme("dark"));
  systemChange(false);
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  view.unmount();
});
