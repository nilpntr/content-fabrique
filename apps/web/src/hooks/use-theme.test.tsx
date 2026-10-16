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
