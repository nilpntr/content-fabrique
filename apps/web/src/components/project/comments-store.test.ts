import { expect, test } from "vitest";
import type { ReviewAnnotation } from "@/components/editor/types";
import { createMemoryCommentsStore } from "./comments-store";
import { isRunId, recallRun, rememberRun } from "./last-run";

const comment = (id: string): ReviewAnnotation => ({
  id,
  version: 1,
  start_ms: 0,
  end_ms: 0,
  kind: "comment",
  body: id,
  status: "open",
  author: { kind: "creator", name: "You" },
  created_at: "2026-10-17T10:00:00Z",
});

test("comments are kept per project, in the order they were added, and the list is a copy", () => {
  const store = createMemoryCommentsStore();
  store.add("prj_a", comment("one"));
  store.add("prj_a", comment("two"));
  store.add("prj_b", comment("other"));
  expect(store.list("prj_a").map((c) => c.id)).toEqual(["one", "two"]);
  expect(store.list("prj_b").map((c) => c.id)).toEqual(["other"]);
  expect(store.list("prj_c")).toEqual([]);
  store.list("prj_a").pop();
  expect(store.list("prj_a")).toHaveLength(2);
});

test("the run of a project is remembered in this browser, and a run id is told from other text", () => {
  window.localStorage.clear();
  expect(recallRun("prj_a")).toBeUndefined();
  rememberRun("prj_a", "run_01jh6m00000000000000000001");
  expect(recallRun("prj_a")).toBe("run_01jh6m00000000000000000001");
  expect(recallRun("prj_b")).toBeUndefined();
  expect(isRunId("run_01jh6m00000000000000000001")).toBe(true);
  expect(isRunId("run_short")).toBe(false);
  expect(isRunId("ast_01jh6m00000000000000000001")).toBe(false);
  expect(isRunId(undefined)).toBe(false);
});
