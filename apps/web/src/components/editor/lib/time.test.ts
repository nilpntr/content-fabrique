import { expect, test } from "vitest";
import { formatRange, formatRulerLabel, formatTimecode, frameToMs, msToFrame, stepFrames } from "./time";

test("timecodes are mm:ss.s, truncated to a tenth", () => {
  expect(formatTimecode(0)).toBe("00:00.0");
  expect(formatTimecode(2000)).toBe("00:02.0");
  expect(formatTimecode(4999)).toBe("00:04.9");
  expect(formatTimecode(61_250)).toBe("01:01.2");
  expect(formatTimecode(-5)).toBe("00:00.0");
});

test("a range is two timecodes with an en dash; a point is one", () => {
  expect(formatRange(2000, 5000)).toBe("00:02.0–00:05.0");
  expect(formatRange(2600, 2600)).toBe("00:02.6");
});

test("ruler labels drop the tenths once ticks are a second apart or more", () => {
  expect(formatRulerLabel(2000, 1000)).toBe("0:02");
  expect(formatRulerLabel(65_000, 5000)).toBe("1:05");
  expect(formatRulerLabel(2500, 500)).toBe("0:02.5");
});

test("frames and milliseconds convert on the frame grid", () => {
  expect(msToFrame(1000, 30)).toBe(30);
  expect(frameToMs(1, 30)).toBe(33);
  expect(frameToMs(msToFrame(2000, 25), 25)).toBe(2000);
});

test("frame steps stay on the grid and inside the clip", () => {
  expect(stepFrames(1000, 30, 1, 5000)).toBe(1033);
  expect(stepFrames(1000, 30, -1, 5000)).toBe(967);
  expect(stepFrames(0, 30, -1, 5000)).toBe(0);
  expect(stepFrames(4990, 30, 10, 5000)).toBe(5000);
});
