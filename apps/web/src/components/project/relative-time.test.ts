import { expect, test } from "vitest";
import { relativeTime } from "./relative-time";

const now = Date.parse("2026-10-17T12:00:00Z");
const ago = (ms: number) => new Date(now - ms).toISOString();

test("a time is said relative to now", () => {
  expect(relativeTime(ago(1000), now)).toBe("just now");
  expect(relativeTime(ago(30_000), now)).toBe("30 seconds ago");
  expect(relativeTime(ago(5 * 60_000), now)).toBe("5 minutes ago");
  expect(relativeTime(ago(3 * 3_600_000), now)).toBe("3 hours ago");
  expect(relativeTime(ago(86_400_000), now)).toBe("yesterday");
  expect(relativeTime(ago(10 * 86_400_000), now)).toBe("10 days ago");
  expect(relativeTime(ago(90 * 86_400_000), now)).toBe("3 months ago");
  expect(relativeTime(ago(800 * 86_400_000), now)).toBe("2 years ago");
  expect(relativeTime("not a date", now)).toBe("");
});
