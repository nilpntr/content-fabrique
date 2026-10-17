import { expect, test } from "vitest";
import type { ReviewAnnotation } from "@/components/editor/types";
import { createMemoryCommentsStore } from "./comments-store";
import { chooseRun, isRunId, recallRun, rememberRun } from "./last-run";

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

test("a project's comments are replaced as a whole, kept per project, and the list is a copy", () => {
  const store = createMemoryCommentsStore();
  store.replace("prj_a", [comment("one"), comment("two")]);
  store.replace("prj_b", [comment("other")]);
  expect(store.list("prj_a").map((c) => c.id)).toEqual(["one", "two"]);
  expect(store.list("prj_b").map((c) => c.id)).toEqual(["other"]);
  expect(store.list("prj_c")).toEqual([]);
  store.list("prj_a").pop();
  expect(store.list("prj_a")).toHaveLength(2);
  store.replace("prj_a", [comment("two")]);
  expect(store.list("prj_a").map((c) => c.id)).toEqual(["two"]);
});

test("the run of a project is remembered in this browser; text that is not a run is ignored", () => {
  window.localStorage.clear();
  expect(recallRun("prj_a")).toBeUndefined();
  rememberRun("prj_a", "run_01jh6m00000000000000000001", "2026-10-17T10:00:00Z");
  expect(recallRun("prj_a")).toEqual({ id: "run_01jh6m00000000000000000001", createdAt: "2026-10-17T10:00:00Z" });
  expect(recallRun("prj_b")).toBeUndefined();
  window.localStorage.setItem("cf:last-run:prj_c", "run_01jh6m00000000000000000001");
  expect(recallRun("prj_c")).toBeUndefined();
  window.localStorage.setItem("cf:last-run:prj_c", JSON.stringify({ id: "not-a-run", createdAt: "2026-10-17T10:00:00Z" }));
  expect(recallRun("prj_c")).toBeUndefined();
  expect(isRunId("run_01jh6m00000000000000000001")).toBe(true);
  expect(isRunId("run_short")).toBe(false);
  expect(isRunId("ast_01jh6m00000000000000000001")).toBe(false);
  expect(isRunId(undefined)).toBe(false);
});

test("the run to show: the URL's, else the newest version's run when it is newer than the remembered one, else the remembered one", () => {
  const A = "run_01jh6m00000000000000000001";
  const B = "run_01jh6m00000000000000000002";
  const C = "run_01jh6m00000000000000000003";
  const remembered = { id: A, createdAt: "2026-10-17T10:00:00Z" };
  expect(chooseRun(C, remembered, { runId: B, createdAt: "2026-10-17T11:00:00Z" })).toBe(C);
  expect(chooseRun("junk", remembered, undefined)).toBe(A);
  expect(chooseRun(undefined, remembered, { runId: B, createdAt: "2026-10-17T11:00:00Z" })).toBe(B);
  expect(chooseRun(undefined, remembered, { runId: B, createdAt: "2026-10-17T09:00:00Z" })).toBe(A);
  expect(chooseRun(undefined, undefined, { runId: B, createdAt: "2026-10-17T09:00:00Z" })).toBe(B);
  expect(chooseRun(undefined, undefined, { runId: "usr_x", createdAt: "2026-10-17T09:00:00Z" })).toBeUndefined();
  expect(chooseRun(undefined, remembered, { runId: "usr_x", createdAt: "2026-10-17T12:00:00Z" })).toBe(A);
  expect(chooseRun(undefined, undefined, undefined)).toBeUndefined();
});
