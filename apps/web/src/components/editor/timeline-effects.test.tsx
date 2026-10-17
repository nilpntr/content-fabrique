import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { mainItems, markersOf, sfxItems, toReviewVersion } from "@/components/project/review-data";
import type { TimelineVersion } from "@/lib/api/client";
import { assetId, v2Spec, versionSummary } from "@/test-fixtures";
import { ReviewScreen } from "./review-screen";
import type { ReviewVersion } from "./types";

const names = { [assetId(1)]: "Lat pulldown", [assetId(2)]: "Seated row", [assetId(3)]: "Posing" };

function version(spec: unknown = v2Spec()): ReviewVersion {
  const full = { ...versionSummary(1), spec, ops: [], diff: [], issues: [] } as unknown as TimelineVersion;
  const read = toReviewVersion({ summary: versionSummary(1), version: full }, "9:16");
  if (!read) throw new Error("the version was not read");
  return read;
}

beforeEach(() => {
  // jsdom has no layout: every box is 1100 px wide, 100 px per second for the 11 s edit.
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({ x: 0, y: 0, left: 0, top: 0, right: 1100, bottom: 1000, width: 1100, height: 1000, toJSON: () => ({}) });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function screenWith(read: ReviewVersion) {
  return render(<ReviewScreen versions={[read]} comments={[]} assetNames={names} src="blob:test-video" />);
}

const details = () => within(screen.getByTestId("timeline-details"));

// ---- The spec, read ---------------------------------------------------------------------------------------------------------

test("the clips of the spec carry their camera move, speed ramp, look, sound mode, transition and the editor's note", () => {
  const items = mainItems(v2Spec());
  expect(items.map((item) => item.id)).toEqual(["c1", "c2", "c3", "c4", "c5", "c6"]);
  const [c1, c2, c3, c4, c5] = items;
  expect(c1).toMatchObject({ motion: { preset: "punch_in", at_ms: 100, strength: 0.15 }, look: { preset: "gym_moody" }, audio: { mode: "bed" }, why: expect.stringContaining("heaviest lockout") });
  expect(c2?.transition_in).toEqual({ type: "whip_left", duration_ms: 250 });
  expect(c3?.speed_ramp).toEqual([
    { at_ms: 0, speed: 2 },
    { at_ms: 2000, speed: 0.5 },
  ]);
  expect(c4).toMatchObject({ look: { preset: "gym_moody", match: true }, audio: { mode: "full", gain_db: -3 }, motion: { preset: "shake", at_ms: 0 } });
  expect(c5).toMatchObject({ audio: { mode: "mute" }, motion: { preset: "push_in" } });
});

test("a clip with no audio settings is silent, the old use_source is full sound, and none-presets are no effect", () => {
  const spec = {
    tracks: [
      {
        type: "video",
        items: [
          { id: "a", asset_id: "ast_1", at_ms: 0, source: { in_ms: 0, out_ms: 1000 } },
          { id: "b", asset_id: "ast_1", at_ms: 1000, source: { in_ms: 0, out_ms: 1000 }, audio: { use_source: true, gain_db: 0 } },
          { id: "c", asset_id: "ast_1", at_ms: 2000, source: { in_ms: 0, out_ms: 1000 }, audio: { mode: "bed" }, motion: { preset: "none" }, look: { preset: "none" } },
          { id: "d", asset_id: "ast_1", at_ms: 3000, source: { in_ms: 0, out_ms: 1000 }, look: { preset: "none", match: true }, audio: { mode: "wild" }, why: "   " },
        ],
      },
    ],
  };
  const [a, b, c, d] = mainItems(spec);
  expect(a?.audio).toEqual({ mode: "mute" });
  expect(b?.audio).toEqual({ mode: "full" });
  expect(c).toMatchObject({ audio: { mode: "bed" } });
  expect(c?.motion).toBeUndefined();
  expect(c?.look).toBeUndefined();
  // A match with no preset is still a look.
  expect(d?.look).toEqual({ preset: "none", match: true });
  expect(d?.audio).toEqual({ mode: "mute" });
  expect(d?.why).toBeUndefined();
});

test("the sound effects and the markers of the spec are read, and the rest of the audio track is not drawn", () => {
  const sfx = sfxItems(v2Spec());
  expect(sfx.map((item) => [item.id, item.sfx, item.variant])).toEqual([
    ["s1", "riser", "tension"],
    ["s2", "impact", "punch"],
    ["s3", "whoosh", "fast"],
    ["s4", "whoosh", "heavy"],
    ["s5", "impact", "crack"],
    ["s6", "sub_drop", "boom"],
    ["s7", "click", undefined],
  ]);
  expect(sfx.find((item) => item.id === "s2")?.gain_db).toBe(2);
  const markers = markersOf(v2Spec());
  expect(markers).toHaveLength(23);
  expect(markers.filter((marker) => marker.kind === "downbeat")).toHaveLength(6);
  expect(markersOf({ markers: [{ at_ms: 10, kind: "wobble" }, { kind: "beat" }, { at_ms: 5, kind: "drop" }] })).toEqual([{ at_ms: 5, kind: "drop" }]);
  // The same marker twice is one.
  expect(markersOf({ markers: [{ at_ms: 5, kind: "downbeat" }, { at_ms: 5, kind: "downbeat" }, { at_ms: 5, kind: "drop" }] })).toHaveLength(2);
  expect(sfxItems({})).toEqual([]);
});

// ---- The timeline ----------------------------------------------------------------------------------------------------------

test("the clips show their camera moves, speed ramp, look and sound mode as badges", () => {
  screenWith(version());
  const tracks = within(screen.getByTestId("timeline-surface"));
  expect(tracks.getByRole("button", { name: "c1: Punch-in" })).toBeTruthy();
  expect(tracks.getByRole("button", { name: "c4: Shake" })).toBeTruthy();
  expect(tracks.getByRole("button", { name: "c5: Push-in" })).toBeTruthy();
  expect(tracks.getByRole("button", { name: "c6: Pull-back" })).toBeTruthy();
  expect(tracks.getByRole("button", { name: "c3: speed ramp" }).getAttribute("data-badge")).toBe("ramp");
  expect(tracks.queryByRole("button", { name: "c1: speed ramp" })).toBeNull();
  // The look is a chip with its name; the match shows on a clip that has it.
  expect(tracks.getByRole("button", { name: "c1: look Gym moody" }).textContent).toBe("Gym moody");
  expect(tracks.queryByRole("button", { name: "c5: look Gym moody" })).toBeNull();
  // Every clip has a sound mode: a bed, full sound or muted.
  const modes = ["c1", "c2", "c3", "c4", "c5", "c6"].map((id) => tracks.getByRole("button", { name: new RegExp(`^${id}: sound `) }).getAttribute("data-mode"));
  expect(modes).toEqual(["bed", "bed", "bed", "full", "mute", "mute"]);
});

test("the transitions are marked between the clips they join, one mark for each kind", () => {
  screenWith(version());
  const marks = within(screen.getByTestId("timeline-surface")).getAllByRole("button", { name: /into c\d$/ });
  expect(marks.map((mark) => mark.getAttribute("aria-label"))).toEqual(["Whip left into c2", "Zoom through into c3", "Flash into c4", "Crossfade into c5", "Dip to black into c6"]);
  // c1 starts the video with a cut: nothing before it.
  expect(screen.queryByRole("button", { name: /into c1$/ })).toBeNull();
});

test("the sound lane has an icon for each effect, named by category and variant, and leaves a trend sound out", () => {
  screenWith(version());
  const lane = within(screen.getByRole("group", { name: "Sound effects" }));
  expect(lane.getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual([
    "Riser (tension) at 00:00.0",
    "Impact (punch) at 00:00.1",
    "Whoosh (fast) at 00:00.9",
    "Whoosh (heavy) at 00:02.6",
    "Impact (crack) at 00:04.5",
    "Sub drop (boom) at 00:04.6",
    "Click at 00:08.0",
  ]);
  expect(lane.getByRole("button", { name: /^Whoosh \(heavy\)/ }).getAttribute("title")).toBe("Whoosh (heavy) · 00:02.6");
  expect(lane.getByRole("button", { name: /^Click/ }).getAttribute("data-category")).toBe("click");
});

test("effects at nearly the same moment go on separate rows", () => {
  const { container } = screenWith(version());
  const top = (id: string) => (container.querySelector(`[data-sfx="${id}"]`) as HTMLElement).style.top;
  // The impact and the sub drop under it start 10 ms apart.
  expect(top("s5")).not.toBe(top("s6"));
  expect(top("s1")).toBe(top("s3"));
});

test("a version with no effects has no sound lane, no beat grid and no marks", () => {
  const plain = version({ tracks: [{ type: "video", items: [{ id: "c1", asset_id: assetId(1), at_ms: 0, source: { in_ms: 0, out_ms: 3000 } }] }] });
  const { container } = screenWith(plain);
  expect(screen.queryByRole("group", { name: "Sound effects" })).toBeNull();
  expect(container.querySelector("[data-beat]")).toBeNull();
  expect(screen.queryByRole("button", { name: /into c\d$/ })).toBeNull();
  // Its clip still says it is silent.
  expect(screen.getByRole("button", { name: "c1: sound muted" })).toBeTruthy();
});

test("the beat grid is ticks on the ruler, taller on the downbeats, and lines through the clips on the downbeats", () => {
  const { container } = screenWith(version());
  const ticks = [...container.querySelectorAll("[data-beat]")];
  expect(ticks.filter((tick) => tick.getAttribute("data-beat") === "downbeat")).toHaveLength(6);
  expect(ticks.filter((tick) => tick.getAttribute("data-beat") === "beat")).toHaveLength(17);
  expect(ticks.find((tick) => tick.getAttribute("data-beat") === "downbeat")?.className).toContain("h-3");
  expect(ticks.find((tick) => tick.getAttribute("data-beat") === "beat")?.className).toContain("h-1.5");
  expect(container.querySelectorAll("[data-downbeat-line]")).toHaveLength(6);
});

test("a grid too dense to read shows its downbeats only", () => {
  const dense = Array.from({ length: 400 }, (_, i) => ({ at_ms: i * 20, kind: i % 4 === 0 ? "downbeat" : "beat" }));
  const read = version({ tracks: [{ type: "video", items: [{ id: "c1", asset_id: assetId(1), at_ms: 0, source: { in_ms: 0, out_ms: 8000 } }] }], markers: dense });
  const { container } = screenWith(read);
  expect(container.querySelectorAll('[data-beat="beat"]')).toHaveLength(0);
  expect(container.querySelectorAll('[data-beat="downbeat"]')).toHaveLength(100);
});

// ---- Reading a mark --------------------------------------------------------------------------------------------------------

test("the details line asks for a mark until one is picked", () => {
  screenWith(version());
  expect(details().getByText(/Point at an effect, a transition or a sound/)).toBeTruthy();
});

test("a clip's handle opens its details, with the editor's note on why it chose it", () => {
  screenWith(version());
  fireEvent.click(screen.getByRole("button", { name: "Details of c1" }));
  const panel = details();
  expect(panel.getByText("Lat pulldown")).toBeTruthy();
  expect(panel.getByText(/The heaviest lockout of the set/)).toBeTruthy();
  expect(panel.getByText("Punch-in at 00:00.1, 15%")).toBeTruthy();
  expect(panel.getByText("Gym moody")).toBeTruthy();
  expect(panel.getByText("Bed")).toBeTruthy();
  // A clip the editor left no note on says so.
  fireEvent.click(screen.getByRole("button", { name: "Details of c2" }));
  expect(details().getByText("The editor left no note for this clip.")).toBeTruthy();
});

test("the badges open the same details with their own part marked, and say what the effect is", () => {
  screenWith(version());
  fireEvent.click(screen.getByRole("button", { name: "c3: speed ramp" }));
  expect(details().getByText("Ramp, 1.08× on average")).toBeTruthy();
  expect(details().getByText(/Fast into slow at the peak/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "c4: sound full sound" }));
  expect(details().getByText("Full sound, -3 dB")).toBeTruthy();
  expect(details().getByText(/for speech/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "c4: Shake" }));
  expect(details().getByText(/camera shake that dies out/)).toBeTruthy();
});

test("a transition's details name the clips it joins and its length", () => {
  screenWith(version());
  fireEvent.click(screen.getByRole("button", { name: "Flash into c4" }));
  expect(details().getByText("150 ms")).toBeTruthy();
  expect(details().getByText(/Between c3 and c4/)).toBeTruthy();
});

test("an effect's details name its category, its variant and its moment", () => {
  screenWith(version());
  fireEvent.click(screen.getByRole("button", { name: /^Impact \(punch\)/ }));
  expect(details().getByText("punch variant")).toBeTruthy();
  expect(details().getByText("at 00:00.1")).toBeTruthy();
  expect(details().getByText(/\+2 dB/)).toBeTruthy();
});

test("pointing at a mark peeks at it and going away puts back what was picked; Close lets go", () => {
  screenWith(version());
  fireEvent.click(screen.getByRole("button", { name: "Details of c1" }));
  const whoosh = screen.getByRole("button", { name: /^Whoosh \(fast\)/ });
  fireEvent.pointerEnter(whoosh);
  expect(details().getByText("fast variant")).toBeTruthy();
  fireEvent.pointerLeave(whoosh);
  expect(details().getByText("Lat pulldown")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Close details" }));
  expect(details().getByText(/Point at an effect/)).toBeTruthy();
  // Picking the same mark twice lets go of it.
  fireEvent.click(screen.getByRole("button", { name: "Details of c1" }));
  fireEvent.click(screen.getByRole("button", { name: "Details of c1" }));
  expect(details().getByText(/Point at an effect/)).toBeTruthy();
});

test("a screen reader is told about a mark that was picked, not about every mark that was pointed at", () => {
  screenWith(version());
  const announcement = () => screen.getByTestId("timeline-announcement").textContent;
  // The details line itself is not a live region.
  expect(screen.getByTestId("timeline-details").getAttribute("aria-live")).toBeNull();
  expect(announcement()).toBe("");

  fireEvent.pointerEnter(screen.getByRole("button", { name: /^Whoosh \(fast\)/ }));
  fireEvent.pointerEnter(screen.getByRole("button", { name: "Flash into c4" }));
  expect(details().getByText("150 ms")).toBeTruthy();
  expect(announcement()).toBe("");

  fireEvent.click(screen.getByRole("button", { name: "Flash into c4" }));
  expect(announcement()).toBe("Flash, 150 milliseconds, between c3 and c4");
  fireEvent.pointerLeave(screen.getByRole("button", { name: "Flash into c4" }));
  expect(announcement()).toBe("Flash, 150 milliseconds, between c3 and c4");

  fireEvent.click(screen.getByRole("button", { name: "Details of c1" }));
  expect(announcement()).toMatch(/^c1 Lat pulldown, sound bed, Punch-in, look Gym moody, why: The heaviest lockout/);
  fireEvent.click(screen.getByRole("button", { name: /^Impact \(punch\)/ }));
  expect(announcement()).toBe("Impact, punch variant, at 00:00.1");
  fireEvent.click(screen.getByRole("button", { name: "Close details" }));
  expect(announcement()).toBe("");
});

test("pressing on a mark does not start a comment selection under it", () => {
  screenWith(version());
  const mark = screen.getByRole("button", { name: "c1: Punch-in" });
  fireEvent.pointerDown(mark, { clientX: 20, clientY: 20, pointerId: 1, button: 0 });
  fireEvent.pointerUp(mark, { clientX: 20, clientY: 20, pointerId: 1, button: 0 });
  expect(screen.getByRole("status", { name: "Selection" }).textContent).toMatch(/Drag to select a range/);
  expect(screen.queryByRole("textbox", { name: "Comment" })).toBeNull();
});
