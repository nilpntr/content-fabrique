import { type RefObject, useCallback, useEffect, useRef, useState } from "react";
import { clamp, stepFrames } from "./lib/time";

export interface PlayerController {
  /** Where the playhead is, in output milliseconds. */
  currentMs: number;
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
}

/**
 * The playhead and the transport. One state for a real `<video>` and for the generated placeholder, so the timeline,
 * the controls and the side panel never care which one is playing.
 *
 * `onSeek` is called for every jump the creator makes (scrub, step, click on a comment), not for playback progress.
 */
export function usePlayerController(options: { durationMs: number; fps: number; onSeek?: (ms: number) => void }): PlayerController {
  const { durationMs, fps } = options;
  const [currentMs, setCurrentMs] = useState(0);
  const [playing, setPlaying] = useState(false);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const msRef = useRef(0);
  const onSeekRef = useRef(options.onSeek);
  onSeekRef.current = options.onSeek;

  const moveTo = useCallback((ms: number) => {
    msRef.current = ms;
    setCurrentMs(ms);
    const video = videoRef.current;
    if (video) video.currentTime = ms / 1000;
  }, []);

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
    setPlaying(false);
  }, []);

  const play = useCallback(() => {
    if (msRef.current >= durationMs) moveTo(0);
    const started = videoRef.current?.play();
    started?.catch(() => setPlaying(false));
    setPlaying(true);
  }, [durationMs, moveTo]);

  const toggle = useCallback(() => (playing ? pause() : play()), [playing, pause, play]);

  const step = useCallback(
    (frames: number) => {
      pause();
      seek(stepFrames(msRef.current, fps, frames, durationMs));
    },
    [fps, durationMs, pause, seek],
  );

  const skip = useCallback((ms: number) => seek(msRef.current + ms), [seek]);

  // The clock: reads the video while it plays, or advances by itself for the placeholder.
  useEffect(() => {
    if (!playing) return;
    let frame = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const video = videoRef.current;
      const elapsed = now - last;
      last = now;
      const t = video ? video.currentTime * 1000 : msRef.current + elapsed;
      if (t >= durationMs || video?.ended) {
        msRef.current = durationMs;
        setCurrentMs(durationMs);
        video?.pause();
        setPlaying(false);
        return;
      }
      msRef.current = t;
      setCurrentMs(Math.round(t));
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [playing, durationMs]);

  // A shorter version can leave the playhead past the end.
  useEffect(() => {
    if (msRef.current > durationMs) moveTo(durationMs);
  }, [durationMs, moveTo]);

  return { currentMs, playing, durationMs, fps, videoRef, seek, play, pause, toggle, step, skip };
}
