import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { DeviceFrame } from "@/components/device-frame";
import { EXAMPLE_PROMPTS } from "./brief-step";
import { formatDuration } from "./duration";
import { FootageStep } from "./footage-step";
import { FormatStep } from "./format-step";
import { NewProjectFlow } from "./new-project-flow";
import { SAMPLE_ASSETS } from "./sample-data";
import type { AssetId, Format } from "./types";

afterEach(cleanup);

const radio = (name: RegExp) => screen.getByRole("radio", { name }) as HTMLInputElement;
const checkbox = (name: RegExp) => screen.getByRole("checkbox", { name }) as HTMLInputElement;
const checkedFormats = () => screen.getAllByRole("radio").filter((r) => (r as HTMLInputElement).checked).map((r) => (r as HTMLInputElement).value);

function Format_({ initial = "9:16" as Format }) {
  let value = initial;
  const spy = vi.fn((format: Format) => {
    value = format;
  });
  const view = render(<FormatStep value={value} onChange={spy} />);
  return { ...view, spy, rerenderWith: () => view.rerender(<FormatStep value={value} onChange={spy} />) };
}

test("the format cards are one radio group with the selected card checked", () => {
  Format_({});
  expect(screen.getByRole("group", { name: "Output format" })).toBeTruthy();
  expect(screen.getAllByRole("radio")).toHaveLength(4);
  expect(checkedFormats()).toEqual(["9:16"]);
  // Name is the format and its label, description carries the size and use.
  const card = radio(/^9:16/);
  expect(card.getAttribute("aria-labelledby")).toBeTruthy();
  const description = document.getElementById(card.getAttribute("aria-describedby") ?? "");
  expect(description?.textContent).toContain("1080×1920");
  expect(description?.textContent).toContain("TikTok, Reels, Shorts");
});

test("arrow keys move the selection and wrap, Home and End jump", () => {
  const { spy, rerenderWith } = Format_({});
  fireEvent.keyDown(radio(/^9:16/), { key: "ArrowRight" });
  expect(spy).toHaveBeenLastCalledWith("1:1");
  rerenderWith();
  expect(document.activeElement).toBe(radio(/^1:1/));
  fireEvent.keyDown(radio(/^1:1/), { key: "ArrowDown" });
  expect(spy).toHaveBeenLastCalledWith("4:5");
  rerenderWith();
  fireEvent.keyDown(radio(/^4:5/), { key: "End" });
  expect(spy).toHaveBeenLastCalledWith("16:9");
  rerenderWith();
  fireEvent.keyDown(radio(/^16:9/), { key: "ArrowRight" });
  expect(spy).toHaveBeenLastCalledWith("9:16");
  rerenderWith();
  fireEvent.keyDown(radio(/^9:16/), { key: "ArrowLeft" });
  expect(spy).toHaveBeenLastCalledWith("16:9");
  rerenderWith();
  fireEvent.keyDown(radio(/^16:9/), { key: "ArrowUp" });
  expect(spy).toHaveBeenLastCalledWith("4:5");
  rerenderWith();
  fireEvent.keyDown(radio(/^4:5/), { key: "Home" });
  expect(spy).toHaveBeenLastCalledWith("9:16");
});

test("Space and Enter select a card, and a click does too", () => {
  const { spy } = Format_({});
  fireEvent.keyDown(radio(/^4:5/), { key: " " });
  expect(spy).toHaveBeenLastCalledWith("4:5");
  fireEvent.keyDown(radio(/^16:9/), { key: "Enter" });
  expect(spy).toHaveBeenLastCalledWith("16:9");
  fireEvent.click(radio(/^1:1/));
  expect(spy).toHaveBeenLastCalledWith("1:1");
});

test("a device frame is decorative and reflects the active state", () => {
  const { container } = render(<DeviceFrame kind="laptop" active />);
  const svg = container.querySelector("svg");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
  expect(svg?.getAttribute("data-active")).toBe("true");
});

function Footage() {
  let ids: AssetId[] = [];
  const spy = vi.fn((next: AssetId[]) => {
    ids = next;
  });
  const view = render(<FootageStep assets={SAMPLE_ASSETS} selectedIds={ids} onChange={spy} />);
  const sync = () => view.rerender(<FootageStep assets={SAMPLE_ASSETS} selectedIds={ids} onChange={spy} />);
  return {
    sync,
    ids: () => ids,
    click: (name: RegExp, init: { shiftKey?: boolean } = {}) => {
      fireEvent.click(checkbox(name), init);
      sync();
    },
  };
}

test("footage: multi-select keeps pick order and shows count and total duration", () => {
  const footage = Footage();
  expect(screen.getByText(/0 clips selected/)).toBeTruthy();
  footage.click(/Gym walk-in/);
  footage.click(/Back double biceps/);
  expect(footage.ids()).toEqual([SAMPLE_ASSETS[4]?.id, SAMPLE_ASSETS[0]?.id]);
  expect(screen.getByText(/2 clips selected/)).toBeTruthy();
  expect(screen.getByText("0:20 total")).toBeTruthy(); // 12 s + 8 s
  expect(checkbox(/Gym walk-in/).checked).toBe(true);
  footage.click(/Gym walk-in/);
  expect(screen.getByText(/1 clip selected/)).toBeTruthy();
});

test("footage: shift-click selects the range from the last clicked clip, and shift-click again deselects it", () => {
  const footage = Footage();
  footage.click(/Lat pulldown/);
  footage.click(/Front lat spread/, { shiftKey: true });
  expect(footage.ids()).toEqual([SAMPLE_ASSETS[1], SAMPLE_ASSETS[2], SAMPLE_ASSETS[3]].map((a) => a?.id));
  // Shift-click backwards from the new anchor, over clips that are already picked.
  footage.click(/Chalk up/);
  footage.click(/Gym walk-in/, { shiftKey: true });
  expect(footage.ids()).toHaveLength(5);
  expect(screen.getByText(/5 clips selected/)).toBeTruthy();
  // The anchor is now "Gym walk-in" (selected): shift-clicking "Lat pulldown" (selected) deselects the range.
  footage.click(/Gym walk-in/);
  footage.click(/Lat pulldown/, { shiftKey: true });
  expect(footage.ids()).toEqual([SAMPLE_ASSETS[5]?.id]);
});

test("footage: select all, clear, search and tag filters", () => {
  const footage = Footage();
  fireEvent.click(screen.getByRole("button", { name: "Select all" }));
  footage.sync();
  expect(footage.ids()).toHaveLength(SAMPLE_ASSETS.length);
  fireEvent.click(screen.getByRole("button", { name: "Clear" }));
  footage.sync();
  expect(footage.ids()).toEqual([]);

  fireEvent.click(screen.getByRole("button", { name: "posing" }));
  expect(screen.getByRole("button", { name: "posing" }).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getAllByRole("checkbox")).toHaveLength(5);
  fireEvent.change(screen.getByRole("searchbox", { name: "Search clips" }), { target: { value: "mirror" } });
  expect(screen.getAllByRole("checkbox")).toHaveLength(2);
  // "Select all" only takes what is shown.
  fireEvent.click(screen.getByRole("button", { name: /Select all 2 shown/ }));
  footage.sync();
  expect(footage.ids()).toHaveLength(2);
  fireEvent.change(screen.getByRole("searchbox", { name: "Search clips" }), { target: { value: "zzz" } });
  expect(screen.getByText("No clips match")).toBeTruthy();
  expect(screen.getByText(/2 hidden by the filter/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));
  expect(screen.getAllByRole("checkbox")).toHaveLength(SAMPLE_ASSETS.length);
});

test("formatDuration", () => {
  expect(formatDuration(0)).toBe("0:00");
  expect(formatDuration(42_000)).toBe("0:42");
  expect(formatDuration(65_400)).toBe("1:05");
  expect(formatDuration(3_723_000)).toBe("1:02:03");
});

const next = () => fireEvent.click(screen.getByRole("button", { name: /^Next/ }));

test("the flow carries the format, the clips and the chip-filled brief into onCreate", () => {
  const onCreate = vi.fn();
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} />);

  // Step 1: format. 9:16 starts selected.
  expect(checkedFormats()).toEqual(["9:16"]);
  fireEvent.click(radio(/^4:5/));
  next();

  // Step 2: footage. The next step needs at least one clip.
  const nextButton = screen.getByRole("button", { name: /^Next/ }) as HTMLButtonElement;
  expect(nextButton.disabled).toBe(true);
  fireEvent.click(checkbox(/Back double biceps/));
  fireEvent.click(checkbox(/Barbell row/));
  expect(nextButton.disabled).toBe(false);
  next();

  // Step 3: the example chips fill the textarea; a second one is added below the first.
  const textarea = screen.getByRole("textbox", { name: /Instructions/ }) as HTMLTextAreaElement;
  expect(textarea.value).toBe("");
  const first = EXAMPLE_PROMPTS[0];
  const second = EXAMPLE_PROMPTS[1];
  fireEvent.click(screen.getByRole("button", { name: first }));
  expect(textarea.value).toBe(first);
  expect(document.activeElement).toBe(textarea);
  fireEvent.click(screen.getByRole("button", { name: second }));
  expect(textarea.value).toBe(`${first}\n\n${second}`);
  fireEvent.change(textarea, { target: { value: `  ${first}  ` } });
  // Idea and sound are placeholders for now.
  expect((screen.getByLabelText(/^Idea/) as HTMLInputElement).disabled).toBe(true);
  expect((screen.getByLabelText(/^Sound/) as HTMLInputElement).disabled).toBe(true);
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  expect(onCreate).toHaveBeenCalledWith({
    format: "4:5",
    assetIds: [SAMPLE_ASSETS[0]?.id, SAMPLE_ASSETS[2]?.id],
    brief: first,
  });
});

test("review summarises the choices and Create project sends the payload once", async () => {
  let finish: () => void = () => {};
  const onCreate = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} />);
  fireEvent.click(radio(/^16:9/));
  next();
  fireEvent.click(checkbox(/Back double biceps/));
  fireEvent.click(checkbox(/Barbell row/));
  next();
  fireEvent.change(screen.getByRole("textbox", { name: /Instructions/ }), { target: { value: "  Open on the pose  " } });
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);

  // The step heading takes focus, and the summary shows each choice.
  expect(document.activeElement?.textContent).toBe("Review and create");
  const summary = within(screen.getByRole("region", { name: "Review and create" }));
  expect(summary.getByText("1920×1080", { exact: false })).toBeTruthy();
  expect(summary.getByText("2 clips")).toBeTruthy();
  expect(summary.getByText("Open on the pose")).toBeTruthy();

  const create = screen.getByRole("button", { name: "Create project" });
  fireEvent.click(create);
  fireEvent.click(create);
  expect(onCreate).toHaveBeenCalledTimes(1);
  expect(onCreate).toHaveBeenCalledWith({
    format: "16:9",
    assetIds: [SAMPLE_ASSETS[0]?.id, SAMPLE_ASSETS[2]?.id],
    brief: "Open on the pose",
  });
  await act(async () => finish());
});

test("an empty brief is sent as an empty string, and a failed create shows an error and can retry", async () => {
  const onCreate = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValueOnce(undefined);
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} />);
  next();
  fireEvent.click(checkbox(/Back double biceps/));
  next();
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  expect(screen.getByText(/None\. The editor will pick/)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/could not be created/));
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(2));
  expect(onCreate).toHaveBeenLastCalledWith({ format: "9:16", assetIds: [SAMPLE_ASSETS[0]?.id], brief: "" });
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
});

test("Edit on the review goes back to that step with the choices kept", () => {
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={vi.fn()} initialFormat="1:1" />);
  next();
  fireEvent.click(checkbox(/Back double biceps/));
  next();
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  fireEvent.click(screen.getByRole("button", { name: "Edit format" }));
  expect(checkedFormats()).toEqual(["1:1"]);
  next();
  expect(checkbox(/Back double biceps/).checked).toBe(true);
});
