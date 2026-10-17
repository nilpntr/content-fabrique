import { expect, test } from "vitest";
import { event, render } from "@/test-fixtures";
import { isRunActive, mergeEvents, pickRender, runPollDelay } from "./project-hooks";

test("a run is active until it has finished, and is polled while it is", () => {
  for (const status of ["queued", "running", "waiting_input"] as const) expect(isRunActive(status)).toBe(true);
  for (const status of ["succeeded", "failed", "cancelled"] as const) expect(isRunActive(status)).toBe(false);
  expect(isRunActive(undefined)).toBe(false);
  // Every 1 to 2 seconds while the editor works; a little slower while it only waits for the creator; never once it has finished.
  expect(runPollDelay("queued")).toBe(1500);
  expect(runPollDelay("running")).toBe(1500);
  expect(runPollDelay("waiting_input")).toBe(2500);
  expect(runPollDelay("succeeded")).toBe(false);
  expect(runPollDelay("failed")).toBe(false);
  expect(runPollDelay("cancelled")).toBe(false);
  expect(runPollDelay(undefined)).toBe(false);
});

test("events accumulate by sequence number, once each and in order", () => {
  const merged = mergeEvents([event(2, "status", "b"), event(1, "status", "a")], [event(3, "status", "c"), event(2, "status", "b again")]);
  expect(merged.map((e) => [e.seq, e.text])).toEqual([
    [1, "a"],
    [2, "b again"],
    [3, "c"],
  ]);
  expect(mergeEvents([], [])).toEqual([]);
});

test("of a version's renders, a ready one is shown, else one in progress, else the latest failure", () => {
  const failed = render({ id: "ren_failed", status: "failed" });
  const working = render({ id: "ren_working", status: "rendering" });
  const ready = render({ id: "ren_ready", status: "ready" });
  // The list is newest first.
  expect(pickRender([failed, working, ready])?.id).toBe("ren_ready");
  expect(pickRender([failed, working])?.id).toBe("ren_working");
  expect(pickRender([failed])?.id).toBe("ren_failed");
  expect(pickRender([])).toBeUndefined();
});
