import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClipCard } from "./clip-card";
import { SAMPLE_ASSETS } from "./sample-data";
import type { Asset } from "./types";
import { HOVER_DELAY_MS, LONG_PRESS_MS, SCRUB_IDLE_MS } from "./use-clip-preview";

const GRID = { cols: 5, rows: 4, intervalMs: 1000, tileW: 100, tileH: 180 };

const READY: Asset = {
  id: "ast_01jh7qtest000000000000001",
  title: "Lat pulldown",
  tags: ["exercise", "back", "cable"],
  status: "ready",
  durationMs: 20_000,
  width: 1080,
  height: 1920,
  createdAt: "2026-10-12T09:00:00.000Z",
  posterUrl: "data:image/svg+xml;utf8,poster",
  hoverPreviewUrl: "blob:http://localhost/preview-a",
  sprite: { url: "data:image/svg+xml;utf8,sprite", grid: GRID },
};
const clip = (overrides: Partial<Asset> = {}): Asset => ({ ...READY, ...overrides });

const play = vi.fn(() => Promise.resolve());
const pause = vi.fn();
const originalMatchMedia = window.matchMedia;

beforeEach(() => {
  vi.useFakeTimers();
  play.mockClear();
  pause.mockClear();
  Object.defineProperty(HTMLMediaElement.prototype, "play", { configurable: true, value: play });
  Object.defineProperty(HTMLMediaElement.prototype, "pause", { configurable: true, value: pause });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  window.matchMedia = originalMatchMedia;
});

const media = (container: HTMLElement | Document = document) => container.querySelector<HTMLElement>('[data-slot="clip-media"]') as HTMLElement;
const videos = () => document.querySelectorAll("video");
const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));
const mouse = { pointerType: "mouse" };

function reduceMotion() {
  window.matchMedia = ((query: string) =>
    ({ matches: query.includes("prefers-reduced-motion"), media: query, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList) as typeof window.matchMedia;
}

function sized(el: HTMLElement, width = 200) {
  el.getBoundingClientRect = () => ({ left: 0, top: 0, right: width, bottom: 150, width, height: 150, x: 0, y: 0, toJSON: () => ({}) });
}

test("no video element and no request exist before the pointer rests on the card", () => {
  render(<ClipCard asset={clip()} />);
  expect(videos()).toHaveLength(0);
  expect(document.querySelector("source")).toBeNull();
  expect(fetch).not.toHaveBeenCalled();
  // Even the poster-only render holds no media URL besides the lazy poster.
  expect(document.body.innerHTML).not.toContain("preview-a");
});

test("the preview video mounts after 300 ms of hover, muted and looping, and nothing earlier", () => {
  render(<ClipCard asset={clip()} />);
  fireEvent.pointerEnter(media(), mouse);
  advance(HOVER_DELAY_MS - 1);
  expect(videos()).toHaveLength(0);
  advance(1);
  const video = document.querySelector("video") as HTMLVideoElement;
  expect(video).not.toBeNull();
  expect(video.muted).toBe(true);
  expect(video.loop).toBe(true);
  expect(video.getAttribute("preload")).toBe("none");
  expect(video.hasAttribute("playsinline")).toBe(true);
  expect(video.getAttribute("src")).toBe("blob:http://localhost/preview-a");
  expect(play).toHaveBeenCalledTimes(1);
  expect(media().getAttribute("data-previewing")).toBe("true");
});

test("leaving the card before the delay cancels the preview", () => {
  render(<ClipCard asset={clip()} />);
  fireEvent.pointerEnter(media(), mouse);
  advance(200);
  fireEvent.pointerLeave(media(), mouse);
  advance(1000);
  expect(videos()).toHaveLength(0);
});

test("leaving the card unmounts the video", () => {
  render(<ClipCard asset={clip()} />);
  fireEvent.pointerEnter(media(), mouse);
  advance(HOVER_DELAY_MS);
  expect(videos()).toHaveLength(1);
  fireEvent.pointerLeave(media(), mouse);
  expect(videos()).toHaveLength(0);
  expect(media().getAttribute("data-previewing")).toBe("false");
});

test("only one card previews at a time", () => {
  render(
    <>
      <ClipCard asset={clip({ id: "ast_01jh7qtest000000000000001", title: "First", hoverPreviewUrl: "blob:a" })} />
      <ClipCard asset={clip({ id: "ast_01jh7qtest000000000000002", title: "Second", hoverPreviewUrl: "blob:b" })} />
    </>,
  );
  const [first, second] = [...document.querySelectorAll<HTMLElement>('[data-slot="clip-media"]')] as [HTMLElement, HTMLElement];
  fireEvent.pointerEnter(first, mouse);
  advance(HOVER_DELAY_MS);
  expect(Array.from(videos(), (v) => v.getAttribute("src"))).toEqual(["blob:a"]);
  // The second card's delay passes while the first has not been left (a keyboard focus plus a pointer, say).
  fireEvent.pointerEnter(second, mouse);
  advance(HOVER_DELAY_MS);
  expect(Array.from(videos(), (v) => v.getAttribute("src"))).toEqual(["blob:b"]);
});

test("a clip that is not ready, or has no preview file, never mounts a video", () => {
  const { rerender } = render(<ClipCard asset={clip({ status: "processing", progress: 10 })} />);
  fireEvent.pointerEnter(media(), mouse);
  advance(2000);
  expect(videos()).toHaveLength(0);
  rerender(<ClipCard asset={clip({ hoverPreviewUrl: undefined })} />);
  fireEvent.pointerLeave(media(), mouse);
  fireEvent.pointerEnter(media(), mouse);
  advance(2000);
  expect(videos()).toHaveLength(0);
});

test("a video that fails to load is dropped and the poster stays", () => {
  render(<ClipCard asset={clip()} />);
  fireEvent.pointerEnter(media(), mouse);
  advance(HOVER_DELAY_MS);
  fireEvent.error(document.querySelector("video") as HTMLVideoElement);
  expect(videos()).toHaveLength(0);
  expect(document.querySelector("img")).not.toBeNull();
});

test("keyboard focus previews like hover, and blur stops it", () => {
  render(<ClipCard asset={clip()} />);
  const button = screen.getByRole("button", { name: "Open Lat pulldown" });
  act(() => button.focus());
  advance(HOVER_DELAY_MS);
  expect(videos()).toHaveLength(1);
  act(() => button.blur());
  expect(videos()).toHaveLength(0);
});

test("moving across the card shows the sprite tile for that position with a progress line", () => {
  render(<ClipCard asset={clip()} />);
  const surface = media();
  sized(surface);
  fireEvent.pointerEnter(surface, { ...mouse, clientX: 0 });
  const frame = () => surface.querySelector<HTMLElement>('[data-slot="sprite-frame"]') as HTMLElement;
  expect(frame().className).toContain("invisible");
  expect(surface.querySelector('[data-slot="scrub-line"]')).toBeNull();

  // A hand resting (2 px of jitter) does not take over from the video.
  fireEvent.pointerMove(surface, { ...mouse, clientX: 2 });
  expect(surface.getAttribute("data-scrubbing")).toBe("false");

  fireEvent.pointerMove(surface, { ...mouse, clientX: 100 });
  expect(surface.getAttribute("data-scrubbing")).toBe("true");
  expect(frame().className).not.toContain("invisible");
  // Half way through 20 s at 1 s a tile is tile 10: column 0, row 2 of 4.
  expect(frame().style.backgroundPosition).toBe("0% 66.6667%");
  expect(frame().style.backgroundSize).toBe("500% 400%");
  const line = surface.querySelector<HTMLElement>('[data-slot="scrub-line"] > div') as HTMLElement;
  expect(line.style.width).toBe("50%");

  fireEvent.pointerMove(surface, { ...mouse, clientX: 200 });
  expect(frame().style.backgroundPosition).toBe("100% 100%");
  expect(line.style.width).toBe("100%");

  advance(SCRUB_IDLE_MS);
  expect(surface.getAttribute("data-scrubbing")).toBe("false");
});

test("the scrub hides the playing video and the video shows again when the pointer rests", () => {
  render(<ClipCard asset={clip()} />);
  const surface = media();
  sized(surface);
  fireEvent.pointerEnter(surface, { ...mouse, clientX: 0 });
  advance(HOVER_DELAY_MS);
  const video = document.querySelector("video") as HTMLVideoElement;
  fireEvent.playing(video);
  expect(video.className).toContain("opacity-100");
  fireEvent.pointerMove(surface, { ...mouse, clientX: 120 });
  expect(pause).toHaveBeenCalled();
  expect(video.className).toContain("opacity-0");
  play.mockClear();
  advance(SCRUB_IDLE_MS);
  expect(play).toHaveBeenCalledTimes(1);
  expect(video.className).toContain("opacity-100");
});

test("a sprite given as a WebVTT track is fetched on first hover only", async () => {
  vi.useRealTimers();
  const vtt = "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\ns.jpg#xywh=0,0,160,90\n\n00:00:10.000 --> 00:00:20.000\ns.jpg#xywh=160,0,160,90\n";
  vi.mocked(fetch).mockResolvedValue(new Response(vtt, { status: 200 }));
  render(<ClipCard asset={clip({ id: "ast_01jh7qtest000000000000009", hoverPreviewUrl: undefined, sprite: { url: "s.jpg", vttUrl: "https://cdn.test/s.vtt" } })} />);
  expect(fetch).not.toHaveBeenCalled();
  const surface = media();
  sized(surface);
  fireEvent.pointerEnter(surface, { ...mouse, clientX: 0 });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe("https://cdn.test/s.vtt");
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  fireEvent.pointerMove(surface, { ...mouse, clientX: 150 });
  const frame = surface.querySelector<HTMLElement>('[data-slot="sprite-frame"]') as HTMLElement;
  // 75 % of 20 s is in the second cue: x 160 of a 320 wide sheet.
  expect(frame.style.backgroundPosition).toBe("100% 0%");
});

test("a long press on touch starts the preview, and the click that follows does not open the clip", () => {
  const onOpen = vi.fn();
  render(<ClipCard asset={clip()} onOpen={onOpen} />);
  const surface = media();
  const touch = { pointerType: "touch", clientX: 50, clientY: 50 };
  fireEvent.pointerDown(surface, touch);
  advance(LONG_PRESS_MS - 1);
  expect(videos()).toHaveLength(0);
  advance(1);
  expect(videos()).toHaveLength(1);
  fireEvent.pointerUp(surface, touch);
  expect(videos()).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Open Lat pulldown" }));
  expect(onOpen).not.toHaveBeenCalled();
  // The next tap opens it.
  fireEvent.pointerDown(surface, touch);
  fireEvent.pointerUp(surface, touch);
  fireEvent.click(screen.getByRole("button", { name: "Open Lat pulldown" }));
  expect(onOpen).toHaveBeenCalledTimes(1);
});

test("a tap on touch opens the clip and starts no preview", () => {
  const onOpen = vi.fn();
  render(<ClipCard asset={clip()} onOpen={onOpen} />);
  const surface = media();
  const touch = { pointerType: "touch", clientX: 50, clientY: 50 };
  fireEvent.pointerEnter(surface, touch);
  fireEvent.pointerDown(surface, touch);
  advance(120);
  fireEvent.pointerUp(surface, touch);
  fireEvent.click(screen.getByRole("button", { name: "Open Lat pulldown" }));
  advance(2000);
  expect(onOpen).toHaveBeenCalledTimes(1);
  expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ title: "Lat pulldown" }));
  expect(videos()).toHaveLength(0);
});

test("a finger that moves before the long press fires is scrolling and cancels it", () => {
  render(<ClipCard asset={clip()} />);
  const surface = media();
  fireEvent.pointerDown(surface, { pointerType: "touch", clientX: 50, clientY: 50 });
  advance(200);
  fireEvent.pointerMove(surface, { pointerType: "touch", clientX: 50, clientY: 90 });
  advance(1000);
  expect(videos()).toHaveLength(0);
});

test("moving the finger after a long press scrubs the sprite", () => {
  render(<ClipCard asset={clip()} />);
  const surface = media();
  sized(surface);
  fireEvent.pointerDown(surface, { pointerType: "touch", clientX: 10, clientY: 50 });
  advance(LONG_PRESS_MS);
  fireEvent.pointerMove(surface, { pointerType: "touch", clientX: 100, clientY: 50 });
  expect(surface.getAttribute("data-scrubbing")).toBe("true");
  expect(surface.querySelector<HTMLElement>('[data-slot="scrub-line"] > div')?.style.width).toBe("50%");
});

test("with reduced motion nothing autoplays, and the sprite scrub still follows the pointer", () => {
  reduceMotion();
  render(<ClipCard asset={clip()} />);
  const surface = media();
  sized(surface);
  fireEvent.pointerEnter(surface, { ...mouse, clientX: 0 });
  advance(5000);
  expect(videos()).toHaveLength(0);
  expect(play).not.toHaveBeenCalled();
  fireEvent.pointerMove(surface, { ...mouse, clientX: 100 });
  expect(surface.getAttribute("data-scrubbing")).toBe("true");
  advance(SCRUB_IDLE_MS);
  expect(surface.getAttribute("data-scrubbing")).toBe("false");
  // A long press does not autoplay either.
  fireEvent.pointerDown(surface, { pointerType: "touch", clientX: 10, clientY: 10 });
  advance(LONG_PRESS_MS + 100);
  expect(videos()).toHaveLength(0);
});

test("status chips and progress for every status", () => {
  const { unmount } = render(<ClipCard asset={clip()} />);
  expect(screen.getByText("Ready")).toBeTruthy();
  expect(screen.getByText("0:20")).toBeTruthy();
  expect(screen.queryByRole("progressbar")).toBeNull();
  expect(screen.getByText("exercise")).toBeTruthy();
  expect(screen.getByText("+2")).toBeTruthy();
  expect(screen.queryByText("back")).toBeNull();
  unmount();

  render(<ClipCard asset={clip({ status: "processing", progress: 42 })} />);
  expect(screen.getByText("Processing 42%")).toBeTruthy();
  const bar = screen.getByRole("progressbar");
  expect(bar.getAttribute("aria-valuenow")).toBe("42");
  expect(bar.getAttribute("data-indeterminate")).toBe("false");
  expect((bar.firstElementChild as HTMLElement).style.width).toBe("42%");
  cleanup();

  render(<ClipCard asset={clip({ status: "processing", progress: undefined })} />);
  expect(screen.getByText("Processing")).toBeTruthy();
  expect(screen.getByRole("progressbar").hasAttribute("aria-valuenow")).toBe(false);
  expect(screen.getByRole("progressbar").getAttribute("data-indeterminate")).toBe("true");
  cleanup();

  render(<ClipCard asset={clip({ status: "uploading", progress: 68 })} />);
  expect(screen.getByText("Uploading 68%")).toBeTruthy();
  expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("68");
  cleanup();

  render(<ClipCard asset={clip({ status: "uploaded" })} />);
  expect(screen.getByText("Queued")).toBeTruthy();
  expect(screen.getByRole("progressbar").getAttribute("data-indeterminate")).toBe("true");
});

test("a failed clip offers Retry, which calls onRetry with the clip", () => {
  const onRetry = vi.fn();
  render(<ClipCard asset={clip({ status: "failed", posterUrl: undefined })} onRetry={onRetry} />);
  expect(screen.getByText("Failed")).toBeTruthy();
  expect(screen.queryByRole("progressbar")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry Lat pulldown" }));
  expect(onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: READY.id }));
});

test("the card keeps the clip's orientation inside one fixed ratio", () => {
  const { unmount } = render(<ClipCard asset={clip()} />);
  expect(media().style.aspectRatio).toMatch(/^1\.3333/);
  const picture = () => document.querySelector<HTMLElement>('[data-slot="clip-picture"]') as HTMLElement;
  expect(picture().style.aspectRatio).toBe("1080 / 1920");
  expect(picture().className).toContain("h-full");
  unmount();
  render(<ClipCard asset={clip({ width: 1920, height: 1080 })} />);
  expect(media().style.aspectRatio).toMatch(/^1\.3333/);
  expect(picture().className).toContain("w-full");
});

async function openMenu(name: string) {
  const trigger = screen.getByRole("button", { name });
  await act(async () => {
    fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
    fireEvent.mouseDown(trigger, { button: 0 });
    fireEvent.pointerUp(trigger, { button: 0, pointerType: "mouse" });
    fireEvent.mouseUp(trigger, { button: 0 });
    fireEvent.click(trigger, { button: 0 });
  });
}

test("Delete in the menu asks first, and only the confirm button calls onDelete", async () => {
  vi.useRealTimers();
  const onDelete = vi.fn();
  render(<ClipCard asset={clip()} onDelete={onDelete} />);
  await openMenu("Actions for Lat pulldown");
  fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("Delete this clip?")).toBeTruthy();
  expect(onDelete).not.toHaveBeenCalled();

  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(onDelete).not.toHaveBeenCalled();
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 50));
  });

  await openMenu("Actions for Lat pulldown");
  fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
  fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete clip" }));
  expect(onDelete).toHaveBeenCalledTimes(1);
  expect(onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: READY.id }));
});

test("the sample library covers every status and has no video files", () => {
  expect(new Set(SAMPLE_ASSETS.map((asset) => asset.status))).toEqual(new Set(["ready", "processing", "uploading", "uploaded", "failed"]));
  for (const asset of SAMPLE_ASSETS) expect(asset.hoverPreviewUrl).toBeUndefined();
  expect(SAMPLE_ASSETS.every((asset) => asset.posterUrl === undefined || asset.posterUrl.startsWith("data:image/svg+xml"))).toBe(true);
});
