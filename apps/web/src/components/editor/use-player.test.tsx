import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { usePlayerController } from "./use-player";

// A frame clock we drive by hand.
let queue: FrameRequestCallback[] = [];
let clock = 0;

beforeEach(() => {
  queue = [];
  clock = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => queue.push(callback));
  vi.stubGlobal("cancelAnimationFrame", () => {
    queue = [];
  });
  vi.spyOn(performance, "now").mockImplementation(() => clock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function frames(count: number, stepMs = 1000 / 60) {
  for (let i = 0; i < count; i++) {
    clock += stepMs;
    const callbacks = queue;
    queue = [];
    act(() => {
      for (const callback of callbacks) callback(clock);
    });
  }
}

test("playback moves the playhead without re-rendering the screen that owns the player", () => {
  let renders = 0;
  const { result } = renderHook(() => {
    renders += 1;
    return usePlayerController({ durationMs: 10_000, fps: 30 });
  });
  act(() => result.current.play());
  const before = renders;

  frames(60);

  expect(result.current.playhead.get()).toBeGreaterThan(900);
  expect(renders).toBe(before);
});

test("listeners hear every change of the playhead, and stop once they unsubscribe", () => {
  const { result } = renderHook(() => usePlayerController({ durationMs: 10_000, fps: 30 }));
  const listener = vi.fn();
  const unsubscribe = result.current.playhead.subscribe(listener);
  act(() => result.current.seek(2000));
  expect(listener).toHaveBeenCalledTimes(1);
  act(() => result.current.seek(2000));
  expect(listener).toHaveBeenCalledTimes(1);
  unsubscribe();
  act(() => result.current.seek(3000));
  expect(listener).toHaveBeenCalledTimes(1);
});

test("a video that replaces the placeholder while playing is moved to the playhead and plays on", () => {
  const { result } = renderHook(() => usePlayerController({ durationMs: 10_000, fps: 30 }));
  act(() => result.current.seek(3000));
  act(() => result.current.play());
  frames(3);

  const video = document.createElement("video");
  const setTime = vi.fn();
  Object.defineProperty(video, "currentTime", { configurable: true, get: () => 0, set: setTime });
  const play = vi.spyOn(video, "play").mockResolvedValue(undefined);

  act(() => result.current.syncMedia(video));
  expect(setTime).toHaveBeenLastCalledWith(expect.closeTo(3.05, 1));
  expect(play).toHaveBeenCalledTimes(1);
});

test("a video that loads while paused is moved to the playhead and stays paused", () => {
  const { result } = renderHook(() => usePlayerController({ durationMs: 10_000, fps: 30 }));
  act(() => result.current.seek(3000));

  const video = document.createElement("video");
  const setTime = vi.fn();
  Object.defineProperty(video, "currentTime", { configurable: true, get: () => 0, set: setTime });
  const play = vi.spyOn(video, "play").mockResolvedValue(undefined);

  act(() => result.current.syncMedia(video));
  expect(setTime).toHaveBeenLastCalledWith(3);
  expect(play).not.toHaveBeenCalled();
});

test("while a new video is still loading, the clock keeps time instead of reading its 0", () => {
  const { result } = renderHook(() => usePlayerController({ durationMs: 10_000, fps: 30 }));
  const video = document.createElement("video");
  // readyState 0 in jsdom: no metadata yet.
  Object.defineProperty(video, "currentTime", { configurable: true, get: () => 0, set: () => {} });
  vi.spyOn(video, "play").mockResolvedValue(undefined);
  result.current.videoRef.current = video;

  act(() => result.current.seek(4000));
  act(() => result.current.play());
  frames(30);
  expect(result.current.playhead.get()).toBeGreaterThan(4400);
});
