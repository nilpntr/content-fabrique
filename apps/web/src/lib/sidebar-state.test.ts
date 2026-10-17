import { expect, test } from "vitest";
import { parseSidebarOpen, sidebarCookie } from "./sidebar-state";

test("the sidebar is open unless the cookie says false", () => {
  expect(parseSidebarOpen(undefined)).toBe(true);
  expect(parseSidebarOpen(null)).toBe(true);
  expect(parseSidebarOpen("")).toBe(true);
  expect(parseSidebarOpen("sidebar_state=true")).toBe(true);
  expect(parseSidebarOpen("sidebar_state=false")).toBe(false);
});

test("finds the cookie among others, with or without spaces", () => {
  expect(parseSidebarOpen("a=1; sidebar_state=false; b=2")).toBe(false);
  expect(parseSidebarOpen("a=1;sidebar_state=false")).toBe(false);
  expect(parseSidebarOpen("theme=dark; sidebar_state=true")).toBe(true);
});

test("ignores cookies that only look similar, and malformed values", () => {
  expect(parseSidebarOpen("my_sidebar_state=false")).toBe(true);
  expect(parseSidebarOpen("sidebar_state_old=false")).toBe(true);
  expect(parseSidebarOpen("sidebar_state")).toBe(true);
  expect(parseSidebarOpen("sidebar_state=banana")).toBe(true);
  expect(parseSidebarOpen("; ;; =false")).toBe(true);
});

test("the written cookie reads back", () => {
  for (const open of [true, false]) {
    const [pair] = sidebarCookie(open).split(";");
    expect(parseSidebarOpen(pair)).toBe(open);
  }
});
