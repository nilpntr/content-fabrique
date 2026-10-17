import { type RefObject, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { clamp, stepFrames } from "./lib/time";

/** The playhead position as a store: playback moves it every frame, and only what draws it listens. */
export interface Playhead {
  /** Output milliseconds, whole. */
  get: () => number;
  subscribe: (listener: () => void) => () => void;
}

export interface PlayerController {
  /** Where the playhead is. Read it in event handlers; to draw it, use `usePlayhead`. */
  playhead: Playhead;
  playing: boolean;
  durationMs: number;
  fps: number;
  /** Attach to the `<video>` element when there is one; without it a clock drives the placeholder. */
  videoRef: RefObject<HTMLVideoElement | null>;
  seek: (ms: number) => void;
  play: () => void;
  pause: () => void;
  toggle: () => void;
  /** Moves the playhead by whole frames and pauses. */
  step: (frames: number) => void;
  /** Moves the playhead by `ms` (negative to go back), keeping the play state. */
  skip: (ms: number) => void;
  /** Call when a `<video>` has loaded (a new source): it is moved to the playhead and resumes if playback was on. */
  syncMedia: (video: HTMLVideoElement) => void;
}

/** Re-renders the caller on every playhead change: use it only in the parts that draw the playhead. */
export function usePlayhead(player: Pick<PlayerController, "playhead">): number {
  return useSyncExternalStore(player.playhead.subscribe, player.playhead.get, () => 0);
}

function createPlayhead() {
  let value = 0;
  const listeners = new Set<() => void>();
  return {
    playhead: {
      get: () => value,
      subscribe: (listener: () => void) => {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      },
    } satisfies Playhead,
    set(ms: number) {
      const next = Math.round(ms);
      if (next === value) return;
      value = next;
      for (const listener of [...listeners]) listener();
    },
  };
}

/**
 * The playhead and the transport. One state for a real `<video>` and for the generated placeholder, so the timeline,
 * the controls and the side panel never care which one is playing.
 *
 * The hook re-renders its host only when `playing`, the duration or the frame rate change. The time itself lives in
 * `player.playhead`, so playback at 60 fps does not render the whole screen.
 *
 * `onSeek` is called for every jump the creator makes (scrub, step, click on a comment), not for playback progress.
 */
export function usePlayerController(options: { durationMs: number; fps: number; onSeek?: (ms: number) => void }): PlayerController {
  const { durationMs, fps } = options;
  const [playing, setPlaying] = useState(false);
  const store = useRef<ReturnType<typeof createPlayhead>>(null);
  store.current ??= createPlayhead();
  const clock = store.current;
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const msRef = useRef(0);
  const playingRef = useRef(false);
  const onSeekRef = useRef(options.onSeek);
  onSeekRef.current = options.onSeek;

  const setPlayingBoth = useCallback((on: boolean) => {
    playingRef.current = on;
    setPlaying(on);
  }, []);

  const moveTo = useCallback(
    (ms: number) => {
      msRef.current = ms;
      clock.set(ms);
      const video = videoRef.current;
      if (video) video.currentTime = ms / 1000;
    },
    [clock],
  );

  const seek = useCallback(
    (ms: number) => {
      const next = clamp(Math.round(ms), 0, durationMs);
      moveTo(next);
      onSeekRef.current?.(next);
    },
    [durationMs, moveTo],
  );

  const pause = useCallback(() => {
    videoRef.current?.pause();
    setPlayingBoth(false);
  }, [setPlayingBoth]);

  const play = useCallback(() => {
    if (msRef.current >= durationMs) moveTo(0);
    videoRef.current?.play()?.catch(() => setPlayingBoth(false));
    setPlayingBoth(true);
  }, [durationMs, moveTo, setPlayingBoth]);

  const toggle = useCallback(() => (playingRef.current ? pause() : play()), [pause, play]);

  const step = useCallback(
    (frames: number) => {
      pause();
      seek(stepFrames(msRef.current, fps, frames, durationMs));
    },
    [fps, durationMs, pause, seek],
  );

  const skip = useCallback((ms: number) => seek(msRef.current + ms), [seek]);

  // A new source, or a video where the placeholder was: a fresh element starts at 0 and paused.
  const syncMedia = useCallback((video: HTMLVideoElement) => {
    video.currentTime = msRef.current / 1000;
    if (playingRef.current) video.play()?.catch(() => setPlayingBoth(false));
  }, [setPlayingBoth]);

  // The clock: reads the video while it plays, or advances by itself for the placeholder (and while a video loads).
  useEffect(() => {
    if (!playing) return;
    let frame = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const video = videoRef.current;
      const elapsed = now - last;
      last = now;
      const t = video && video.readyState >= 1 ? video.currentTime * 1000 : msRef.current + elapsed;
      if (t >= durationMs || video?.ended) {
        msRef.current = durationMs;
        clock.set(durationMs);
        video?.pause();
        setPlayingBoth(false);
        return;
      }
      msRef.current = t;
      clock.set(t);
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [playing, durationMs, clock, setPlayingBoth]);

  // A shorter version can leave the playhead past the end.
  useEffect(() => {
    if (msRef.current > durationMs) moveTo(durationMs);
  }, [durationMs, moveTo]);

  return useMemo(
    () => ({ playhead: clock.playhead, playing, durationMs, fps, videoRef, seek, play, pause, toggle, step, skip, syncMedia }),
    [clock, playing, durationMs, fps, seek, play, pause, toggle, step, skip, syncMedia],
  );
}
