import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
  expect(within(panel()).getByText("on Version 1")).toBeTruthy();

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
  expect(within(pulldown()).getByText("on Version 1")).toBeTruthy();
  expect(within(text).getByText("00:04.3–00:05.9")).toBeTruthy();
  expect(within(text).getByText("Addressed in Version 2")).toBeTruthy();
  expect(within(text).getByText(/Was/).textContent).toContain("00:09.0–00:11.0");

  // An unmapped comment is not drawn on the timeline; a mapped one is.
  expect(document.querySelector('[data-region-id="ann_pulldown"]')).toBeNull();
  expect(document.querySelector('[data-region-id="ann_text"]')).toBeTruthy();

  // Back on v1 the pulldown comment maps again.
  fireEvent.click(screen.getByRole("button", { name: "Version 1" }));
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

const sampleScreen = (props: Partial<Parameters<typeof ReviewScreen>[0]> = {}) =>
  render(<ReviewScreen versions={SAMPLE_VERSIONS} comments={SAMPLE_COMMENTS} assetNames={SAMPLE_ASSET_NAMES} {...props} />);
const itemOf = (text: RegExp | string) => within(panel()).getByText(text).closest("li") as HTMLElement;

test("Space presses a focused button and does not toggle playback; elsewhere it plays", () => {
  renderScreen();
  const next = screen.getByRole("button", { name: "Next frame" });
  const onButton = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
  next.dispatchEvent(onButton);
  expect(onButton.defaultPrevented).toBe(false);
  expect(HTMLMediaElement.prototype.play).not.toHaveBeenCalled();

  const onPage = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
  document.body.dispatchEvent(onPage);
  expect(onPage.defaultPrevented).toBe(true);
  expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
});

test("resolved and applied comments are not open: own label, no count, not in the Open filter, neutral on the timeline", () => {
  const base = { version: 1, start_ms: 1000, end_ms: 2000, kind: "comment", author: { kind: "creator", name: "Sam" } } as const;
  renderScreen({
    comments: [
      { ...base, id: "o", body: "still open", status: "open", created_at: "2026-10-14T09:03:00Z" },
      { ...base, id: "r", body: "closed by hand", status: "resolved", created_at: "2026-10-14T09:02:00Z" },
      { ...base, id: "a", body: "applied somewhere", status: "applied", created_at: "2026-10-14T09:01:00Z" },
    ],
  });
  expect(within(panel()).getByText("1 open · 3 total")).toBeTruthy();
  expect(within(itemOf("closed by hand")).getByText("Resolved")).toBeTruthy();
  expect(within(itemOf("applied somewhere")).getByText("Applied")).toBeTruthy();
  expect(document.querySelector('[data-region-id="r"]')?.getAttribute("data-tone")).toBe("neutral");
  expect(document.querySelector('[data-region-id="a"]')?.getAttribute("data-tone")).toBe("neutral");
  expect(document.querySelector('[data-region-id="o"]')?.getAttribute("data-tone")).toBe("open");

  fireEvent.click(within(panel()).getByRole("button", { name: "Open" }));
  expect(within(panel()).getAllByRole("listitem")).toHaveLength(1);
});

test("on an older version, a comment from a newer one says it was made later, not that it no longer maps", () => {
  sampleScreen();
  fireEvent.click(screen.getByRole("button", { name: "Version 1" }));
  const logo = itemOf("Put the logo here.");
  expect(logo.getAttribute("data-status")).toBe("newer");
  expect(within(logo).getByText("Made on Version 3 (newer)")).toBeTruthy();
  expect(within(panel()).queryAllByText("No longer mapped")).toHaveLength(0);
  expect(document.querySelector('[data-region-id="ann_logo"]')).toBeNull();
});

test("Open vN switches to that version and seeks to the comment's start there", () => {
  const onSeek = vi.fn();
  sampleScreen({ onSeek });
  const pulldown = itemOf("This pulldown clip drags. Cut it.");
  fireEvent.click(within(pulldown).getByRole("button", { name: "Open Version 1" }));

  expect(screen.getByRole("button", { name: "Version 1" }).getAttribute("aria-pressed")).toBe("true");
  expect(onSeek).toHaveBeenLastCalledWith(4500);
  expect(screen.getByRole("status", { name: "Playhead" }).textContent).toContain("00:04.5");
  expect(itemOf("This pulldown clip drags. Cut it.").getAttribute("data-selected")).toBe("true");
});

test("a new drag while a comment has text keeps the old range and the text", () => {
  renderScreen();
  drag(200, 500);
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "half written" } });

  drag(600, 800);
  const dialog = screen.getByRole("dialog", { name: "Add comment" });
  expect(within(dialog).getByText("00:02.0–00:05.0")).toBeTruthy();
  expect((within(dialog).getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe("half written");
  expect(within(dialog).getByRole("status").textContent).toContain("Save or cancel");
  // The refused selection did not move the playhead either.
  expect(screen.getByRole("status", { name: "Playhead" }).textContent).toContain("00:02.0");

  // A click for a point is a new selection too.
  pointer("pointerDown", 900);
  pointer("pointerUp", 900);
  expect(within(screen.getByRole("dialog")).getByText("00:02.0–00:05.0")).toBeTruthy();

  // Once cancelled, the next drag starts a fresh comment with no text.
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  drag(600, 800);
  const fresh = screen.getByRole("dialog", { name: "Add comment" });
  expect(within(fresh).getByText("00:06.0–00:08.0")).toBeTruthy();
  expect((within(fresh).getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe("");
});

test("a new drag while the comment box is still empty starts over", () => {
  renderScreen();
  drag(200, 500);
  drag(600, 800);
  expect(within(screen.getByRole("dialog")).getByText("00:06.0–00:08.0")).toBeTruthy();
});

test("with typed text, picking another comment or version leaves the draft where it is", () => {
  sampleScreen();
  pointer("pointerDown", 300);
  pointer("pointerUp", 300);
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "typed" } });
  const range = within(screen.getByRole("dialog")).getByText(/^\d\d:\d\d\.\d$/).textContent;

  fireEvent.click(screen.getByRole("button", { name: "Version 1" }));
  expect(screen.getByRole("button", { name: "Version 3" }).getAttribute("aria-pressed")).toBe("true");
  expect(within(screen.getByRole("dialog")).getByRole("status").textContent).toContain("Save or cancel");

  fireEvent.click(within(panel()).getByText(/b-roll cuts in too early/));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText(range as string)).toBeTruthy();
  expect((within(dialog).getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe("typed");
});

test("with no versions the screen shows an empty state instead of crashing", () => {
  render(<ReviewScreen versions={[]} comments={[]} />);
  expect(screen.getByText("No video yet")).toBeTruthy();
});

test("an initial version that does not exist falls back to the latest", () => {
  sampleScreen({ initialVersion: 9 });
  expect(screen.getByRole("button", { name: "Version 3" }).getAttribute("aria-pressed")).toBe("true");
  cleanup();
  sampleScreen({ initialVersion: 2 });
  expect(screen.getByRole("button", { name: "Version 2" }).getAttribute("aria-pressed")).toBe("true");
});

test("zooming keeps the middle of the view, and the playhead follow does not undo it", () => {
  const offsets = new WeakMap<Element, number>();
  Object.defineProperty(HTMLElement.prototype, "scrollLeft", {
    configurable: true,
    get(this: Element) {
      return offsets.get(this) ?? 0;
    },
    set(this: Element, value: number) {
      offsets.set(this, value);
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 1000 });
  try {
    renderScreen();
    const viewport = screen.getByTestId("timeline-surface").parentElement?.parentElement as HTMLElement;

    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    // 4x: 400 px per second. Seeking far to the right brings the playhead into view.
    scrubTo(9000);
    expect(viewport.scrollLeft).toBe(3100);

    // Zooming in again keeps 9 s in the middle: 8x is 800 px per second, so 9 s is at 7200 px.
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    expect(viewport.scrollLeft).toBe(6700);
  } finally {
    Reflect.deleteProperty(HTMLElement.prototype, "scrollLeft");
    Reflect.deleteProperty(HTMLElement.prototype, "clientWidth");
  }
});

// ── Comments kept by a parent (P1-08) ───────────────────────────────────────

const saved = (id: string, over: Partial<ReviewAnnotation> = {}): ReviewAnnotation => ({
  id,
  version: 1,
  start_ms: 1000,
  end_ms: 2000,
  kind: "comment",
  body: `Body of ${id}`,
  status: "open",
  author: { kind: "creator", name: "You", own: true },
  created_at: "2026-10-14T09:30:00Z",
  ...over,
});
const item = (id: string) => within(panel()).getByText(`Body of ${id}`).closest("li") as HTMLElement;

test("a comment gets Resolve, Edit and Delete only when the parent handles them, and Edit and Delete only for the author's", () => {
  const { unmount } = renderScreen({ comments: [saved("ann_1"), saved("ann_2", { author: { kind: "creator", name: "Teammate" } })] });
  expect(within(panel()).queryByRole("button", { name: "Resolve" })).toBeNull();
  unmount();

  renderScreen({ comments: [saved("ann_1"), saved("ann_2", { author: { kind: "creator", name: "Teammate" } })], onCommentUpdate: vi.fn(), onCommentDelete: vi.fn() });
  expect(within(item("ann_1")).getByRole("button", { name: "Resolve" })).toBeTruthy();
  expect(within(item("ann_1")).getByRole("button", { name: "Edit" })).toBeTruthy();
  expect(within(item("ann_1")).getByRole("button", { name: "Delete" })).toBeTruthy();
  expect(within(item("ann_2")).getByRole("button", { name: "Resolve" })).toBeTruthy();
  expect(within(item("ann_2")).queryByRole("button", { name: "Edit" })).toBeNull();
  expect(within(item("ann_2")).queryByRole("button", { name: "Delete" })).toBeNull();
});

test("resolve, reopen, edit and delete change the list at once and are reported to the parent", () => {
  const onCommentUpdate = vi.fn();
  const onCommentDelete = vi.fn();
  const onCommentsChange = vi.fn();
  renderScreen({
    comments: [saved("ann_1"), saved("ann_1r", { reply_to: "ann_1", author: { kind: "agent", name: "Editor" } }), saved("ann_2", { status: "applied", applied_in_version: 2 })],
    onCommentUpdate,
    onCommentDelete,
    onCommentsChange,
  });

  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Resolve" }));
  expect(onCommentUpdate).toHaveBeenLastCalledWith("ann_1", { status: "resolved" });
  expect(item("ann_1").getAttribute("data-status")).toBe("resolved");
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Reopen" }));
  expect(onCommentUpdate).toHaveBeenLastCalledWith("ann_1", { status: "open" });
  expect(item("ann_1").getAttribute("data-status")).toBe("open");

  // Editing what the editor applied reopens it.
  fireEvent.click(within(item("ann_2")).getByRole("button", { name: "Edit" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Edit comment text" }), { target: { value: "  Actually the other way " } });
  fireEvent.click(within(screen.getByRole("form", { name: "Edit comment" })).getByRole("button", { name: "Save" }));
  expect(onCommentUpdate).toHaveBeenLastCalledWith("ann_2", { body: "Actually the other way" });
  expect(within(panel()).getByText("Actually the other way").closest("li")?.getAttribute("data-status")).toBe("open");
  // Cancelling changes nothing.
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Edit" }));
  fireEvent.click(within(screen.getByRole("form", { name: "Edit comment" })).getByRole("button", { name: "Cancel" }));
  expect(onCommentUpdate).toHaveBeenCalledTimes(3);

  // Delete asks twice, and takes the reply with it.
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Delete" }));
  expect(onCommentDelete).not.toHaveBeenCalled();
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Keep" }));
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Delete" }));
  fireEvent.click(within(item("ann_1")).getByRole("button", { name: "Delete for good" }));
  expect(onCommentDelete).toHaveBeenCalledExactlyOnceWith("ann_1");
  expect(within(panel()).queryByText("Body of ann_1")).toBeNull();
  expect(within(panel()).queryByText("Body of ann_1r")).toBeNull();
  expect(onCommentsChange.mock.lastCall?.[0]).toHaveLength(1);
});

test("a comment that is still being saved has no actions; a new list from the parent replaces the screen's, and the error shows", () => {
  const onDismissCommentsError = vi.fn();
  const handlers = { onCommentUpdate: vi.fn(), onCommentDelete: vi.fn(), onDismissCommentsError };
  const { rerender } = render(<ReviewScreen versions={[VERSION]} comments={[saved("ann_local_1"), saved("ann_1")]} src="blob:test-video" {...handlers} />);
  expect(within(item("ann_local_1")).queryByRole("button", { name: "Resolve" })).toBeNull();
  expect(within(item("ann_1")).getByRole("button", { name: "Resolve" })).toBeTruthy();

  // The parent took the first one back (the API refused it) and says why.
  rerender(<ReviewScreen versions={[VERSION]} comments={[saved("ann_1")]} src="blob:test-video" commentsError="Your role can't comment." {...handlers} />);
  expect(within(panel()).queryByText("Body of ann_local_1")).toBeNull();
  expect(within(panel()).getByRole("alert").textContent).toContain("Your role can't comment.");
  fireEvent.click(within(panel()).getByRole("button", { name: "Dismiss" }));
  expect(onDismissCommentsError).toHaveBeenCalledTimes(1);
});

test("a saved comment keeps the selection when the API's id replaces the screen's", async () => {
  let resolve: (value: { id: string }) => void = () => {};
  const onCommentCreate = vi.fn((_comment: ReviewAnnotation) => new Promise<{ id: string }>((r) => (resolve = r)));
  const { rerender } = renderScreen({ onCommentCreate });
  scrubTo(4000);
  key("c");
  saveComment("Make this text bigger");
  expect(within(panel()).getByText("Make this text bigger").closest("li")?.getAttribute("data-selected")).toBe("true");
  const temp = onCommentCreate.mock.calls[0]?.[0] as ReviewAnnotation;

  await act(async () => {
    resolve({ id: "ann_real" });
  });
  rerender(<ReviewScreen versions={[VERSION]} comments={[{ ...temp, id: "ann_real" }]} src="blob:test-video" onCommentCreate={onCommentCreate} />);
  expect(within(panel()).getByText("Make this text bigger").closest("li")?.getAttribute("data-comment-id")).toBe("ann_real");
  expect(within(panel()).getByText("Make this text bigger").closest("li")?.getAttribute("data-selected")).toBe("true");
});
