import { expect, test } from "vitest";
import { greeting, projectStatusWords, videosIn, videosLeftText, videosShort, versionName } from "./words";

const credits = (available: number, over: Partial<{ allowance: number; period_end: string | null; credits_per_video: number }> = {}) => ({ credits_per_video: 30, available, allowance: 150, period_end: "2099-11-14T00:00:00Z", low: false, top_ups: [], ...over });

test("credits are shown as videos, by the price list's credits per video", () => {
  expect(videosIn(0, 30)).toBe(0);
  expect(videosIn(29, 30)).toBe(0);
  expect(videosIn(30, 30)).toBe(1);
  expect(videosIn(127, 30)).toBe(4);
  expect(videosLeftText(credits(127))).toBe("≈ 4 videos left this month");
  expect(videosLeftText(credits(30))).toBe("≈ 1 video left this month");
  // Without a monthly allowance (a trial, top-ups only) there is no "this month".
  expect(videosLeftText(credits(60, { allowance: 0, period_end: null }))).toBe("≈ 2 videos left");
  expect(videosLeftText(credits(6))).toBe("Enough for a few changes");
  expect(videosLeftText(credits(0))).toBe("No videos left");
  expect(videosShort(credits(95))).toBe("3 videos");
  expect(videosShort(credits(6))).toBe("<1 video");
  expect(videosShort(credits(0))).toBe("0 videos");
});

test("the number of credits in a video is the API's, not the app's", () => {
  // The same balance is a different number of videos when the price list says a video costs another number of credits.
  expect(videosLeftText(credits(100, { credits_per_video: 10 }))).toBe("≈ 10 videos left this month");
  expect(videosLeftText(credits(100, { credits_per_video: 40 }))).toBe("≈ 2 videos left this month");
  expect(videosShort(credits(100, { credits_per_video: 25 }))).toBe("4 videos");
});

test("a project's status is said in plain words", () => {
  const summary = (status: string, over: { asset_count?: number; current_timeline_version?: number | null } = {}) =>
    ({ status, asset_count: 3, current_timeline_version: null, ...over }) as Parameters<typeof projectStatusWords>[0];
  expect(projectStatusWords(summary("review", { current_timeline_version: 2 }))).toEqual({ label: "Ready to watch", tone: "ready" });
  expect(projectStatusWords(summary("editing"))).toEqual({ label: "Editing…", tone: "busy" });
  expect(projectStatusWords(summary("draft", { asset_count: 0 }))).toEqual({ label: "Needs your footage", tone: "attention" });
  expect(projectStatusWords(summary("draft"))).toEqual({ label: "Not edited yet", tone: "neutral" });
  expect(projectStatusWords(summary("exported"))).toEqual({ label: "Exported", tone: "neutral" });
  expect(projectStatusWords(summary("archived")).label).toBe("archived");
});

test("versions have names, and the greeting follows the clock", () => {
  expect(versionName(3)).toBe("Version 3");
  expect(greeting(new Date(2026, 9, 18, 8), "Gino")).toBe("Good morning, Gino");
  expect(greeting(new Date(2026, 9, 18, 13), " ")).toBe("Good afternoon");
  expect(greeting(new Date(2026, 9, 18, 21))).toBe("Good evening");
});
