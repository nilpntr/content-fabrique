const pad = (value: number) => String(value).padStart(2, "0");

/** "mm:ss.s", truncated to a tenth like a player's clock: 2000 becomes "00:02.0". */
export function formatTimecode(ms: number): string {
  const tenths = Math.max(0, Math.floor(ms / 100 + 1e-9));
  const minutes = Math.floor(tenths / 600);
  const seconds = Math.floor((tenths % 600) / 10);
  return `${pad(minutes)}:${pad(seconds)}.${tenths % 10}`;
}

/** "mm:ss.s–mm:ss.s", or one timecode for a point. */
export function formatRange(startMs: number, endMs: number): string {
  return endMs > startMs ? `${formatTimecode(startMs)}–${formatTimecode(endMs)}` : formatTimecode(startMs);
}

/** A ruler label: "0:02", or "0:02.5" when the ticks are closer than a second. */
export function formatRulerLabel(ms: number, stepMs: number): string {
  const tenths = Math.round(Math.max(0, ms) / 100);
  const minutes = Math.floor(tenths / 600);
  const seconds = Math.floor((tenths % 600) / 10);
  const label = `${minutes}:${pad(seconds)}`;
  return stepMs >= 1000 ? label : `${label}.${tenths % 10}`;
}

export const msToFrame = (ms: number, fps: number) => Math.round((ms * fps) / 1000);
export const frameToMs = (frame: number, fps: number) => Math.round((frame * 1000) / fps);

export const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

/** The time `frames` frames away from `ms`, on the frame grid and inside the clip. */
export function stepFrames(ms: number, fps: number, frames: number, durationMs: number): number {
  return clamp(frameToMs(msToFrame(ms, fps) + frames, fps), 0, durationMs);
}
