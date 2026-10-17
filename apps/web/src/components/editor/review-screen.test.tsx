import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { buildTimeMap } from "./lib/time-map";
import { ReviewScreen } from "./review-screen";
import { SAMPLE_ASSET_NAMES, SAMPLE_COMMENTS, SAMPLE_VERSIONS } from "./sample-data";
import type { ReviewAnnotation, ReviewVersion, VideoItem } from "./types";

// Ten seconds, two clips with a cut at 5 s. jsdom has no layout, so every box is 1000 px wide: 100 px per second.
const items: VideoItem[] = [
  { id: "c1", asset_id: "ast_one", at_ms: 0, source: { in_ms: 0, out_ms: 5000 }, speed: 1 },
  { id: "c2", asset_id: "ast_two", at_ms: 5000, source: { in_ms: 0, out_ms: 5000 }, speed: 1 },
];
const VERSION: ReviewVersion = {
  version: 1,
  created_at: "2026-10-14T09:00:00Z",
  author_kind: "agent",
  summary: "First edit",
  output: { format: "9:16", fps: 30 },
  duration_ms: 10_000,
  items,
  time_map: buildTimeMap(items),
};

const seekedTo = vi.fn<(seconds: number) => void>();

beforeEach(() => {
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 1000,
    bottom: 1000,
    width: 1000,
    height: 1000,
    toJSON: () => ({}),
  });
  // jsdom plays nothing. Record where the player is told to go.
  Object.defineProperty(HTMLMediaElement.prototype, "currentTime", {
    configurable: true,
    get: () => 0,
    set: (seconds: number) => seekedTo(seconds),
  });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  seekedTo.mockClear();
  Reflect.deleteProperty(HTMLMediaElement.prototype, "currentTime");
});

function renderScreen(props: Partial<Parameters<typeof ReviewScreen>[0]> = {}) {
  return render(<ReviewScreen versions={[VERSION]} comments={[]} src="blob:test-video" {...props} />);
}

const key = (name: string, target: Window | Element = window) => fireEvent.keyDown(target, { key: name });
const scrubTo = (ms: number) => fireEvent.change(screen.getByRole("slider", { name: "Scrub" }), { target: { value: String(ms) } });
const selection = () => screen.getByRole("status", { name: "Selection" }).textContent;
const panel = () => screen.getByRole("complementary", { name: "Comment history" });

function pointer(type: "pointerDown" | "pointerMove" | "pointerUp", clientX: number) {
  fireEvent[type](screen.getByTestId("timeline-surface"), { clientX, clientY: 10, pointerId: 1, button: 0 });
}

function drag(fromPx: number, toPx: number) {
  pointer("pointerDown", fromPx);
  pointer("pointerMove", (fromPx + toPx) / 2);
  pointer("pointerMove", toPx);
  pointer("pointerUp", toPx);
}

function saveComment(body: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: body } });
  fireEvent.click(screen.getByRole("button", { name: /Save comment/ }));
}

test("dragging across 0:02 to 0:05 creates a range comment in the side panel, and clicking it seeks there and highlights the range", () => {
  const onSeek = vi.fn();
  renderScreen({ onSeek });

  drag(200, 500);
  expect(within(screen.getByRole("dialog", { name: "Add comment" })).getByText("00:02.0–00:05.0")).toBeTruthy();
  saveComment("Tighten this shot");

  const item = within(panel()).getByText("Tighten this shot").closest("li");
  expect(item).toBeTruthy();
  expect(within(panel()).getByText("00:02.0–00:05.0")).toBeTruthy();
  expect(within(item as HTMLElement).getByText("Open")).toBeTruthy();
  expect(within(panel()).getByText("made on v1")).toBeTruthy();

  // Move the playhead away, then click the comment in the panel.
  scrubTo(8000);
  seekedTo.mockClear();
  onSeek.mockClear();
  fireEvent.click(within(panel()).getByText("Tighten this shot"));

  expect(onSeek).toHaveBeenCalledTimes(1);
  expect(onSeek).toHaveBeenCalledWith(2000);
  expect(seekedTo).toHaveBeenCalledWith(2);
  expect(screen.getByRole("status", { name: "Playhead" }).textContent).toContain("00:02.0");

  const region = document.querySelector("[data-region-id]");
  expect(region?.getAttribute("data-selected")).toBe("true");
  expect(item?.getAttribute("data-selected")).toBe("true");
});

test("the drag snaps to the cut when the pointer is within 8 px of it", () => {
  renderScreen();
  drag(200, 497);
  expect(selection()).toContain("00:05.0");
});

test("a click without a drag places a point comment", () => {
  renderScreen();
  pointer("pointerDown", 300);
  pointer("pointerUp", 300);
  expect(within(screen.getByRole("dialog", { name: "Add comment" })).getByText("Point")).toBeTruthy();
  saveComment("What is this?");
  expect(within(panel()).getByText("00:03.0")).toBeTruthy();
});

test("Escape cancels a drag in progress and a selection", () => {
  renderScreen();
  pointer("pointerDown", 200);
  pointer("pointerMove", 500);
  key("Escape");
  pointer("pointerUp", 500);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(selection()).not.toMatch(/\d\d:\d\d/);

  drag(200, 500);
  expect(screen.getByRole("dialog", { name: "Add comment" })).toBeTruthy();
  key("Escape", screen.getByRole("textbox", { name: "Comment" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(selection()).not.toMatch(/\d\d:\d\d/);
});

test("the range has two handles that can be adjusted from the keyboard", () => {
  renderScreen();
  drag(200, 500);
  const end = screen.getByRole("slider", { name: "Selection end" });
  expect(end.getAttribute("aria-valuenow")).toBe("5000");
  fireEvent.keyDown(end, { key: "ArrowLeft", shiftKey: true });
  expect(end.getAttribute("aria-valuenow")).toBe("4667");
  expect(screen.getByRole("slider", { name: "Selection start" }).getAttribute("aria-valuenow")).toBe("2000");
});

test("dragging a handle moves that end of the range and snaps to a cut", () => {
  renderScreen();
  drag(200, 400);
  const end = screen.getByRole("slider", { name: "Selection end" });
  fireEvent.pointerDown(end, { clientX: 400, pointerId: 1, button: 0 });
  fireEvent.pointerMove(end, { clientX: 497, pointerId: 1 });
  fireEvent.pointerUp(end, { clientX: 497, pointerId: 1 });
  expect(selection()).toContain("00:02.0");
  expect(selection()).toContain("00:05.0");
});

test("I and O set the in and out points at the playhead", () => {
  renderScreen();
  scrubTo(3000);
  key("i");
  scrubTo(7000);
  key("o");
  expect(selection()).toContain("00:03.0");
  expect(selection()).toContain("00:07.0");
  expect(selection()).toContain("4.0 s");
  expect(screen.queryByRole("dialog")).toBeNull();

  // C then opens the comment box for that range.
  key("c");
  expect(within(screen.getByRole("dialog", { name: "Add comment" })).getByText("00:03.0–00:07.0")).toBeTruthy();
});

test("typing in the comment box does not trigger the player shortcuts", () => {
  renderScreen();
  scrubTo(3000);
  key("i");
  key("c");
  const box = screen.getByRole("textbox", { name: "Comment" });
  seekedTo.mockClear();
  for (const letter of ["i", "o", "k", "l", ",", " "]) key(letter, box);
  expect(seekedTo).not.toHaveBeenCalled();
  expect(selection()).toContain("00:03.0");
  expect(selection()).toContain("00:10.0");
});

test("the transport keys step frames, jump and play", () => {
  const onSeek = vi.fn();
  renderScreen({ onSeek });
  key(".");
  expect(onSeek).toHaveBeenLastCalledWith(33);
  key(",");
  expect(onSeek).toHaveBeenLastCalledWith(0);
  key("l");
  expect(onSeek).toHaveBeenLastCalledWith(5000);
  key("j");
  expect(onSeek).toHaveBeenLastCalledWith(0);
  key(" ");
  expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
  key("k");
  expect(HTMLMediaElement.prototype.pause).toHaveBeenCalled();
});

test("pin mode places a normalised pin on the frame and attaches it to the comment", () => {
  const onCommentCreate = vi.fn<(comment: ReviewAnnotation) => void>();
  renderScreen({ onCommentCreate });
  scrubTo(4000);

  fireEvent.click(screen.getByRole("button", { name: "Pin" }));
  fireEvent.click(screen.getByRole("button", { name: "Place a pin on the frame" }), { clientX: 500, clientY: 250, detail: 1 });
  expect(document.querySelector('[data-pin="draft"]')).toBeTruthy();
  expect(within(screen.getByRole("dialog", { name: "Add comment" })).getByText(/pin on frame/)).toBeTruthy();
  saveComment("Make this text bigger");

  expect(onCommentCreate).toHaveBeenCalledTimes(1);
  expect(onCommentCreate.mock.calls[0]?.[0]).toMatchObject({
    version: 1,
    start_ms: 4000,
    end_ms: 4000,
    pin: { x: 0.5, y: 0.25 },
    body: "Make this text bigger",
    status: "open",
    author: { kind: "creator" },
  });

  // Selecting the comment shows its pin on the frame.
  scrubTo(9000);
  fireEvent.click(within(panel()).getByText("Make this text bigger"));
  expect(document.querySelector('[data-pin="selected"]')).toBeTruthy();
});

test("the Open filter shows open comments only and All shows every one, newest first", () => {
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={SAMPLE_COMMENTS} assetNames={SAMPLE_ASSET_NAMES} />);

  const bodies = () => within(panel()).getAllByRole("listitem").map((item) => item.getAttribute("data-comment-id"));
  expect(bodies()).toHaveLength(SAMPLE_COMMENTS.length);
  expect(bodies()[0]).toBe("ann_logo");

  fireEvent.click(within(panel()).getByRole("button", { name: "Open" }));
  expect(bodies()).toEqual(["ann_logo", "ann_broll", "ann_hook"]);

  fireEvent.click(within(panel()).getByRole("button", { name: "All" }));
  expect(bodies()).toHaveLength(SAMPLE_COMMENTS.length);
});

test("earlier comments stay in the panel: mapped onto the current version, or marked no longer mapped", () => {
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={SAMPLE_COMMENTS} assetNames={SAMPLE_ASSET_NAMES} />);

  const pulldown = () => within(panel()).getByText("This pulldown clip drags. Cut it.").closest("li") as HTMLElement;
  const text = within(panel()).getByText("Make this text bigger.").closest("li") as HTMLElement;

  // On v3, c2 is gone. The text comment from v1 sits on c3, which moved and got faster.
  expect(pulldown().getAttribute("data-status")).toBe("unmapped");
  expect(within(pulldown()).getByText("No longer mapped")).toBeTruthy();
  expect(within(pulldown()).getByText("made on v1")).toBeTruthy();
  expect(within(text).getByText("00:04.3–00:05.9")).toBeTruthy();
  expect(within(text).getByText("Addressed in v2")).toBeTruthy();
  expect(within(text).getByText(/Was/).textContent).toContain("00:09.0–00:11.0");

  // An unmapped comment is not drawn on the timeline; a mapped one is.
  expect(document.querySelector('[data-region-id="ann_pulldown"]')).toBeNull();
  expect(document.querySelector('[data-region-id="ann_text"]')).toBeTruthy();

  // Back on v1 the pulldown comment maps again.
  fireEvent.click(screen.getByRole("button", { name: /^v1/ }));
  expect(pulldown().getAttribute("data-status")).toBe("addressed");
  expect(within(pulldown()).getByText("00:04.5–00:06.5")).toBeTruthy();
  expect(document.querySelector('[data-region-id="ann_pulldown"]')).toBeTruthy();
});

test("hovering a region highlights its panel item, and hovering the item highlights the region", () => {
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={SAMPLE_COMMENTS} assetNames={SAMPLE_ASSET_NAMES} />);
  const region = document.querySelector('[data-region-id="ann_broll"]') as HTMLElement;
  const item = within(panel()).getByText(/b-roll cuts in too early/).closest("li") as HTMLElement;

  fireEvent.pointerEnter(region);
  expect(item.getAttribute("data-hovered")).toBe("true");
  fireEvent.pointerLeave(region);
  expect(item.getAttribute("data-hovered")).toBe("false");

  fireEvent.pointerEnter(item);
  expect(region.getAttribute("data-hovered")).toBe("true");
});

test("clicking a region on the timeline selects the comment and seeks to its start", () => {
  const onSeek = vi.fn();
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={SAMPLE_COMMENTS} assetNames={SAMPLE_ASSET_NAMES} onSeek={onSeek} />);
  fireEvent.click(document.querySelector('[data-region-id="ann_broll"]') as HTMLElement);
  expect(onSeek).toHaveBeenLastCalledWith(7200);
  expect(within(panel()).getByText(/b-roll cuts in too early/).closest("li")?.getAttribute("data-selected")).toBe("true");
});

test("without a src the screen plays a generated placeholder instead of a video", () => {
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={[]} />);
  expect(document.querySelector("video")).toBeNull();
  expect(screen.getByTestId("placeholder-frame")).toBeTruthy();
});
