import type { Sprite, SpriteGrid } from "./types";

/** A frame inside the sprite sheet, in sheet pixels. */
export interface SpriteTile {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** One line of a WebVTT thumbnails track: the frame to show from `startMs` up to `endMs`. */
export interface SpriteCue extends SpriteTile {
  startMs: number;
  endMs: number;
}

export interface SpriteSheet {
  width: number;
  height: number;
}

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

/** Where the pointer is across the element, 0 at its left edge and 1 at its right edge. */
export function scrubFraction(clientX: number, rect: { left: number; width: number }): number {
  if (!(rect.width > 0)) return 0;
  return clamp((clientX - rect.left) / rect.width, 0, 1);
}

/** The clip time under a fraction of the card width. */
export function fractionToMs(fraction: number, durationMs: number): number {
  return clamp(fraction, 0, 1) * Math.max(0, durationMs);
}

/** How many frames a grid sprite holds for a clip: one per interval, never more than the grid has room for. */
export function gridFrameCount(grid: SpriteGrid, durationMs: number): number {
  return clamp(Math.ceil(durationMs / grid.intervalMs), 1, Math.max(1, grid.cols * grid.rows));
}

/** The tile of a regular-grid sprite for a clip time. Times past the last frame show the last frame. */
export function gridTile(grid: SpriteGrid, timeMs: number, durationMs: number): SpriteTile {
  const index = clamp(Math.floor(timeMs / grid.intervalMs), 0, gridFrameCount(grid, durationMs) - 1);
  return { x: (index % grid.cols) * grid.tileW, y: Math.floor(index / grid.cols) * grid.tileH, w: grid.tileW, h: grid.tileH };
}

export function gridSheet(grid: SpriteGrid): SpriteSheet {
  return { width: grid.cols * grid.tileW, height: grid.rows * grid.tileH };
}

/** The cue that covers `timeMs`; the first before the track starts and the last after it ends. */
export function cueTile(cues: readonly SpriteCue[], timeMs: number): SpriteCue | undefined {
  if (cues.length === 0) return undefined;
  return cues.find((cue) => timeMs >= cue.startMs && timeMs < cue.endMs) ?? (timeMs < (cues[0]?.startMs ?? 0) ? cues[0] : cues.at(-1));
}

/** The sheet size implied by the cues: the far edge of the furthest tile. */
export function cuesSheet(cues: readonly SpriteCue[]): SpriteSheet {
  return { width: Math.max(0, ...cues.map((cue) => cue.x + cue.w)), height: Math.max(0, ...cues.map((cue) => cue.y + cue.h)) };
}

const TIMESTAMP = /^(?:(\d+):)?(\d{1,2}):(\d{2})(?:[.,](\d{1,3}))?$/;

function parseTimestamp(text: string): number | null {
  const match = TIMESTAMP.exec(text.trim());
  if (!match) return null;
  const [, hours = "0", minutes = "0", seconds = "0", fraction = "0"] = match;
  return ((Number(hours) * 60 + Number(minutes)) * 60 + Number(seconds)) * 1000 + Number(fraction.padEnd(3, "0"));
}

/** Parses a WebVTT thumbnails track (`sprite.jpg#xywh=x,y,w,h` payloads). Lines it does not understand are skipped. */
export function parseSpriteVtt(text: string): SpriteCue[] {
  const cues: SpriteCue[] = [];
  const lines = text.replace(/^﻿/, "").split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    const arrow = line.indexOf("-->");
    if (arrow < 0) continue;
    const startMs = parseTimestamp(line.slice(0, arrow));
    const endMs = parseTimestamp(line.slice(arrow + 3).trim().split(/\s+/)[0] ?? "");
    const xywh = /#xywh=(\d+),(\d+),(\d+),(\d+)/.exec(lines[i + 1] ?? "");
    if (startMs === null || endMs === null || !xywh) continue;
    cues.push({ startMs, endMs, x: Number(xywh[1]), y: Number(xywh[2]), w: Number(xywh[3]), h: Number(xywh[4]) });
  }
  return cues;
}

/** CSS that shows just `tile` of a sprite sheet inside an element that has the tile's aspect ratio. */
export function tileBackground(tile: SpriteTile, sheet: SpriteSheet): { backgroundSize: string; backgroundPosition: string } {
  const pct = (value: number) => `${Math.round(value * 1e4) / 1e4}%`;
  const size = (sheetSide: number, tileSide: number) => (tileSide > 0 ? (sheetSide / tileSide) * 100 : 100);
  // background-position percentages are relative to the leftover space, so the first tile is 0% and the last is 100%.
  const position = (offset: number, sheetSide: number, tileSide: number) => (sheetSide > tileSide ? (offset / (sheetSide - tileSide)) * 100 : 0);
  return {
    backgroundSize: `${pct(size(sheet.width, tile.w))} ${pct(size(sheet.height, tile.h))}`,
    backgroundPosition: `${pct(position(tile.x, sheet.width, tile.w))} ${pct(position(tile.y, sheet.height, tile.h))}`,
  };
}

/** What the card needs to draw the scrub frame for a position along the card. */
export interface SpriteModel {
  url: string;
  at: (fraction: number) => { tile: SpriteTile; sheet: SpriteSheet };
}

/** Null while a VTT track has not loaded (or has no usable cues). */
export function spriteModel(sprite: Sprite, durationMs: number, cues: readonly SpriteCue[] | null): SpriteModel | null {
  if (sprite.grid) {
    const grid = sprite.grid;
    const sheet = gridSheet(grid);
    return { url: sprite.url, at: (fraction) => ({ tile: gridTile(grid, fractionToMs(fraction, durationMs), durationMs), sheet }) };
  }
  if (!cues || cues.length === 0) return null;
  const sheet = cuesSheet(cues);
  return {
    url: sprite.url,
    at: (fraction) => {
      const cue = cueTile(cues, fractionToMs(fraction, durationMs));
      return { tile: cue ?? { x: 0, y: 0, w: sheet.width, h: sheet.height }, sheet };
    },
  };
}
