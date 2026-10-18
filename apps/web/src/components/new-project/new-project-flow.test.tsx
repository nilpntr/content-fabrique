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

test("the radios share one name and have unique ids, so the browser handles the arrow keys", () => {
  Format_({});
  const radios = screen.getAllByRole("radio") as HTMLInputElement[];
  expect(new Set(radios.map((r) => r.name)).size).toBe(1);
  expect(radios[0]?.name).not.toBe("");
  const ids = radios.flatMap((r) => [r.getAttribute("aria-labelledby"), r.getAttribute("aria-describedby")]);
  expect(new Set(ids).size).toBe(8);
});

test("Home and End select the first and last card; arrows, Enter and modified keys are left alone", () => {
  const { spy, rerenderWith } = Format_({ initial: "1:1" });
  fireEvent.keyDown(radio(/^1:1/), { key: "End" });
  expect(spy).toHaveBeenLastCalledWith("16:9");
  rerenderWith();
  expect(document.activeElement).toBe(radio(/^16:9/));
  fireEvent.keyDown(radio(/^16:9/), { key: "Home" });
  expect(spy).toHaveBeenLastCalledWith("9:16");
  spy.mockClear();
  for (const init of [{ key: "End", ctrlKey: true }, { key: "Home", metaKey: true }, { key: "End", altKey: true }, { key: "Home", shiftKey: true }]) {
    expect(fireEvent.keyDown(radio(/^9:16/), init)).toBe(true);
  }
  for (const key of ["ArrowRight", "ArrowDown", "ArrowLeft", "ArrowUp", "Enter"]) {
    expect(fireEvent.keyDown(radio(/^9:16/), { key })).toBe(true); // not prevented: the browser's own behaviour runs
  }
  expect(spy).not.toHaveBeenCalled();
});

test("choosing a radio (what the browser does on an arrow key or Space) selects that format", () => {
  const { spy } = Format_({});
  fireEvent.click(radio(/^4:5/));
  expect(spy).toHaveBeenLastCalledWith("4:5");
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

test("footage: select all, clear, search and label filters", () => {
  const footage = Footage();
  fireEvent.click(screen.getByRole("button", { name: "Select all" }));
  footage.sync();
  expect(footage.ids()).toHaveLength(SAMPLE_ASSETS.length);
  fireEvent.click(screen.getByRole("button", { name: "Clear" }));
  footage.sync();
  expect(footage.ids()).toEqual([]);

  // Three compact filters with the values the clips have and their counts, not a chip per word.
  const type = screen.getByRole("combobox", { name: "Type" }) as HTMLSelectElement;
  expect(screen.getByRole("combobox", { name: "Body part" })).toBeTruthy();
  expect(screen.getByRole("combobox", { name: "Angle" })).toBeTruthy();
  expect([...type.options].map((o) => o.text)).toEqual(["Type", "Lifting (7)", "Posing (3)", "B-roll (2)", "Before and after (1)", "Product (1)", "Slow motion (1)", "Talking (1)"]);
  expect(screen.queryAllByRole("button", { name: "posing" })).toHaveLength(0);
  fireEvent.change(type, { target: { value: "pose" } });
  expect(screen.getAllByRole("checkbox")).toHaveLength(3);
  // The other filters count what this one leaves: the poses are two front views and a back view.
  const angle = screen.getByRole("combobox", { name: "Angle" }) as HTMLSelectElement;
  expect([...angle.options].map((o) => o.text)).toEqual(["Angle", "Front view (2)", "Back view (1)"]);
  // The search finds words of the title, the exercise and the labels.
  fireEvent.change(screen.getByRole("searchbox", { name: "Search clips" }), { target: { value: "front" } });
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
  expect((screen.getByRole("combobox", { name: "Type" }) as HTMLSelectElement).value).toBe("");
});

test("footage: a card shows the exercise and at most two labels", () => {
  Footage();
  // The exercise the quick look named is the heading; the full title stays as the tooltip.
  const curl = checkbox(/Hammer curl/);
  const card = curl.closest("li") as HTMLElement;
  expect(card.textContent).toContain("Hammer curl");
  expect(card.querySelector("[title='Hammer curls, slow motion']")).toBeTruthy();
  const badges = [...card.querySelectorAll("[data-slot=facet-badge]")].map((b) => b.textContent);
  expect(badges).toEqual(["Slow motion", "Arms"]);
  for (const box of screen.getAllByRole("checkbox")) {
    expect((box.closest("li") as HTMLElement).querySelectorAll("[data-slot=facet-badge]").length).toBeLessThanOrEqual(2);
  }
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
  const textarea = screen.getByRole("textbox", { name: /^What you want/ }) as HTMLTextAreaElement;
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
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
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
  fireEvent.change(screen.getByRole("textbox", { name: /^What you want/ }), { target: { value: "  Open on the pose  " } });
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);

  // The step heading takes focus, and the summary shows each choice.
  expect(document.activeElement?.textContent).toBe("Review and start");
  const summary = within(screen.getByRole("region", { name: "Review and start" }));
  expect(summary.getByText("1920×1080", { exact: false })).toBeTruthy();
  expect(summary.getByText("2 clips")).toBeTruthy();
  expect(summary.getByText("Open on the pose")).toBeTruthy();

  const create = screen.getByRole("button", { name: "Make my video" });
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
  expect(screen.getByText(/None\. I'll decide from your clips/)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/could not be started/));
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
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

test("footage: a stale pick is dropped from every count, and hidden picks only count clips that exist", () => {
  const onChange = vi.fn();
  const gone = "ast_01jh6m99999999999999999999" as AssetId;
  const { rerender } = render(<FootageStep assets={SAMPLE_ASSETS} selectedIds={[SAMPLE_ASSETS[0]?.id as AssetId, gone]} onChange={onChange} />);
  expect(screen.getByText(/1 clip selected/)).toBeTruthy();
  expect(screen.queryByText(/hidden by the filter/)).toBeNull();
  fireEvent.change(screen.getByRole("searchbox", { name: "Search clips" }), { target: { value: "zzz" } });
  expect(screen.getByText(/1 hidden by the filter/)).toBeTruthy();
  rerender(<FootageStep assets={SAMPLE_ASSETS.slice(1)} selectedIds={[SAMPLE_ASSETS[0]?.id as AssetId]} onChange={onChange} />);
  expect(screen.getByText(/0 clips selected/)).toBeTruthy();
  expect(screen.queryByText(/hidden by the filter/)).toBeNull();
});

test("footage: the shift-click anchor is reset by Clear, Select all and by a selection that came from outside", () => {
  const ids = SAMPLE_ASSETS.map((a) => a.id);
  const onChange = vi.fn();
  const view = (selectedIds: AssetId[]) => <FootageStep assets={SAMPLE_ASSETS} selectedIds={selectedIds} onChange={onChange} />;
  const { rerender } = render(view([]));

  fireEvent.click(checkbox(/Lat pulldown/)); // anchor: clip 2
  fireEvent.click(screen.getByRole("button", { name: /^Select all/ }));
  rerender(view(ids));
  fireEvent.click(screen.getByRole("button", { name: "Clear" }));
  rerender(view([]));
  onChange.mockClear();
  fireEvent.click(checkbox(/Back squat/), { shiftKey: true }); // no anchor: just this clip
  expect(onChange).toHaveBeenLastCalledWith([ids[7]]);

  rerender(view([ids[7] as AssetId]));
  fireEvent.click(checkbox(/Gym walk-in/)); // anchor: clip 5
  rerender(view([ids[7] as AssetId, ids[4] as AssetId]));
  rerender(view([ids[0] as AssetId])); // the parent replaced the selection
  fireEvent.click(checkbox(/^Deadlift$/), { shiftKey: true });
  expect(onChange).toHaveBeenLastCalledWith([ids[0], ids[6]]);
});

test("create locks the flow while pending, keeps focus on the button, and keeps the error until the user acts", async () => {
  let fail: () => void = () => {};
  let finish: () => void = () => {};
  const onCreate = vi
    .fn()
    .mockImplementationOnce(() => new Promise<void>((_, reject) => { fail = () => reject(new Error("boom")); }))
    .mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} />);
  next();
  fireEvent.click(checkbox(/Back double biceps/));
  next();
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);

  const create = screen.getByRole("button", { name: "Make my video" }) as HTMLButtonElement;
  create.focus();
  fireEvent.click(create);
  expect(onCreate).toHaveBeenCalledTimes(1);
  const pending = screen.getByRole("button", { name: "Starting…" }) as HTMLButtonElement;
  expect(pending).toBe(create);
  expect(pending.getAttribute("aria-disabled")).toBe("true");
  expect(document.activeElement).toBe(pending);
  expect(screen.getByRole("status").textContent).toContain("Starting");
  for (const name of ["Back", "Edit format", "Edit footage", "Edit instructions"]) {
    expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
  }
  const steps = within(screen.getByRole("navigation", { name: "Progress" })).getAllByRole("button");
  expect(steps).toHaveLength(4);
  for (const step of steps) {
    expect((step as HTMLButtonElement).disabled).toBe(true);
  }
  fireEvent.click(pending); // pressing again while pending does nothing
  expect(onCreate).toHaveBeenCalledTimes(1);

  fail();
  await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
  const again = screen.getByRole("button", { name: "Make my video" });
  expect(document.activeElement).toBe(again);
  expect((screen.getByRole("button", { name: "Back" }) as HTMLButtonElement).disabled).toBe(false);
  // The error stays while the user does nothing, and goes once they act.
  expect(screen.getByRole("alert")).toBeTruthy();
  fireEvent.click(again);
  await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(2));
  await act(async () => finish());
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
});

test("brief: an example that would not fit is disabled with a hint and never truncated; the counter describes the textarea", () => {
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={vi.fn()} />);
  next();
  fireEvent.click(checkbox(/Back double biceps/));
  next();
  const textarea = screen.getByRole("textbox", { name: /^What you want/ }) as HTMLTextAreaElement;
  const describedBy = (textarea.getAttribute("aria-describedby") ?? "").split(" ");
  expect(describedBy).toHaveLength(2);
  expect(describedBy.some((id) => document.getElementById(id)?.textContent?.includes("/ 2000"))).toBe(true);
  expect(screen.getAllByRole("status")[0]?.textContent).toBe("");

  const first = EXAMPLE_PROMPTS[0];
  fireEvent.change(textarea, { target: { value: "x".repeat(2000 - first.length - 1) } }); // one short of fitting after the blank line
  const chip = screen.getByRole("button", { name: new RegExp(`^${first.slice(0, 20)}`) }) as HTMLButtonElement;
  expect(chip.disabled).toBe(true);
  expect(chip.textContent).toContain("Not enough room");
  fireEvent.click(chip);
  expect(textarea.value).toHaveLength(2000 - first.length - 1);
  expect(screen.getAllByRole("status")[0]?.textContent).toBe("Fewer than 200 characters left");
  expect((screen.getByRole("button", { name: new RegExp(`^${EXAMPLE_PROMPTS[1].slice(0, 20)}`) }) as HTMLButtonElement).disabled).toBe(false);
});
