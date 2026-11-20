import { act, cleanup, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ClipFeedback, Shot, ShotList } from "@/lib/api/client";
import { installApi, json, renderApp } from "@/test-api";
import { assetId, projectId } from "@/test-fixtures";
import { ShotListCard } from "./shot-list";
import { clipTime, feedbackWords, shotLine } from "./shot-words";

afterEach(cleanup);

const feedback = (over: Partial<ClipFeedback>): ClipFeedback => ({
  id: "cfb_01j9z3x7m8c4v6n2q5r0t1y8ab",
  asset_id: assetId(1),
  kind: "never_use",
  start_ms: 12_000,
  end_ms: 15_500,
  note: "",
  given_on: { project_id: projectId(), timeline_version: 3, shot_number: 1, item_id: "c1" },
  created_at: "2026-11-20T10:00:00Z",
  ...over,
});

const shot = (number: number, over: Partial<Shot> = {}): Shot => ({
  number,
  item_id: `c${number}`,
  asset_id: assetId(number),
  asset_title: number === 1 ? "Squat" : "Row",
  at_ms: (number - 1) * 2000,
  end_ms: number * 2000,
  source_in_ms: 12_000 * number,
  source_out_ms: 12_000 * number + 2000,
  feedback: [],
  ...over,
});

test("the words of the shot list", () => {
  expect(clipTime(12_400)).toBe("0:12");
  expect(clipTime(65_000)).toBe("1:05");
  expect(shotLine({ asset_title: "Squat", source_in_ms: 12_000 })).toBe("Squat · from 0:12");
  expect(shotLine({ asset_title: null, source_in_ms: 0 })).toBe("A clip that was removed · from 0:00");
  expect(feedbackWords(feedback({}))).toBe("Won't be used again (0:12 to 0:15)");
  expect(feedbackWords(feedback({ kind: "start_later", start_ms: 30_000, end_ms: 32_000 }))).toBe("Starts 2 s later from now on");
});

test("feedback on a shot is given by its number and can be taken back", async () => {
  let list: ShotList = { project_id: projectId(), timeline_version: 3, shots: [shot(1), shot(2)] };
  const api = installApi({
    "GET /v1/projects/{id}/timelines/{version}/shots": () => list,
    "POST /v1/clip-feedback": (call) => {
      const body = call.body as { shot_number: number; kind: ClipFeedback["kind"] };
      const given = feedback({ kind: body.kind });
      list = { ...list, shots: list.shots.map((s) => (s.number === body.shot_number ? { ...s, feedback: [given] } : s)) };
      return given;
    },
    "DELETE /v1/clip-feedback/{id}": () => {
      list = { ...list, shots: list.shots.map((s) => ({ ...s, feedback: [] })) };
      return json(204, null);
    },
  });
  renderApp(<ShotListCard projectId={projectId()} version={3} />);
  const rows = await screen.findAllByTestId("shot");
  expect(rows).toHaveLength(2);
  expect(rows[0]?.textContent).toContain("Squat · from 0:12");

  await act(async () => {
    fireEvent.click(within(rows[0] as HTMLElement).getByRole("button", { name: "Never use again" }));
  });
  expect(api.callsTo("POST /v1/clip-feedback")[0]?.body).toEqual({ project_id: projectId(), timeline_version: 3, shot_number: 1, kind: "never_use" });
  expect(await screen.findByText("Won't be used again (0:12 to 0:15)")).toBeTruthy();

  await act(async () => {
    fireEvent.change(within(rows[1] as HTMLElement).getByLabelText("Start shot 2 later"), { target: { value: "2" } });
  });
  expect(api.callsTo("POST /v1/clip-feedback")[1]?.body).toEqual({ project_id: projectId(), timeline_version: 3, shot_number: 2, kind: "start_later", offset_ms: 2000 });

  const undo = await screen.findAllByRole("button", { name: "Undo" });
  await act(async () => {
    fireEvent.click(undo[0] as HTMLElement);
  });
  expect(api.callsTo("DELETE /v1/clip-feedback/{id}")).toHaveLength(1);
});
