import { expect, test } from "vitest";
import {
  cueTile,
  cuesSheet,
  fractionToMs,
  gridFrameCount,
  gridSheet,
  gridTile,
  parseSpriteVtt,
  scrubFraction,
  spriteModel,
  tileBackground,
} from "./scrub";
import type { SpriteGrid } from "./types";

const GRID: SpriteGrid = { cols: 5, rows: 4, intervalMs: 1000, tileW: 100, tileH: 180 };

test("the scrub fraction is the pointer across the card, clamped to the edges", () => {
  const rect = { left: 50, width: 200 };
  expect(scrubFraction(50, rect)).toBe(0);
  expect(scrubFraction(150, rect)).toBe(0.5);
  expect(scrubFraction(250, rect)).toBe(1);
  expect(scrubFraction(-40, rect)).toBe(0);
  expect(scrubFraction(900, rect)).toBe(1);
  expect(scrubFraction(100, { left: 0, width: 0 })).toBe(0);
});

test("a fraction maps to a clip time", () => {
  expect(fractionToMs(0.25, 8000)).toBe(2000);
  expect(fractionToMs(2, 8000)).toBe(8000);
  expect(fractionToMs(-1, 8000)).toBe(0);
});

test("a grid sprite is read row by row, one tile per interval", () => {
  expect(gridTile(GRID, 0, 20_000)).toEqual({ x: 0, y: 0, w: 100, h: 180 });
  expect(gridTile(GRID, 999, 20_000)).toEqual({ x: 0, y: 0, w: 100, h: 180 });
  expect(gridTile(GRID, 1000, 20_000)).toEqual({ x: 100, y: 0, w: 100, h: 180 });
  expect(gridTile(GRID, 5000, 20_000)).toEqual({ x: 0, y: 180, w: 100, h: 180 });
  expect(gridTile(GRID, 12_000, 20_000)).toEqual({ x: 200, y: 360, w: 100, h: 180 });
});

test("the end of the clip shows the last frame, not a blank one", () => {
  expect(gridFrameCount(GRID, 20_000)).toBe(20);
  expect(gridTile(GRID, 20_000, 20_000)).toEqual({ x: 400, y: 540, w: 100, h: 180 });
  // A clip shorter than the grid only uses the first tiles: 3.2 s at 1 s per tile is 4 frames.
  expect(gridFrameCount(GRID, 3200)).toBe(4);
  expect(gridTile(GRID, 3200, 3200)).toEqual({ x: 300, y: 0, w: 100, h: 180 });
  // A clip longer than the grid holds clamps to the last tile.
  expect(gridTile(GRID, 90_000, 90_000)).toEqual({ x: 400, y: 540, w: 100, h: 180 });
});

test("a tile becomes background-size and position percentages", () => {
  const sheet = gridSheet(GRID);
  expect(sheet).toEqual({ width: 500, height: 720 });
  expect(tileBackground({ x: 0, y: 0, w: 100, h: 180 }, sheet)).toEqual({ backgroundSize: "500% 400%", backgroundPosition: "0% 0%" });
  expect(tileBackground({ x: 400, y: 540, w: 100, h: 180 }, sheet)).toEqual({ backgroundSize: "500% 400%", backgroundPosition: "100% 100%" });
  expect(tileBackground({ x: 200, y: 360, w: 100, h: 180 }, sheet)).toEqual({ backgroundSize: "500% 400%", backgroundPosition: "50% 66.6667%" });
  // A one-tile sheet has no leftover space to position in.
  expect(tileBackground({ x: 0, y: 0, w: 100, h: 180 }, { width: 100, height: 180 })).toEqual({ backgroundSize: "100% 100%", backgroundPosition: "0% 0%" });
});

const VTT = `WEBVTT

00:00:00.000 --> 00:00:02.000
sprite.jpg#xywh=0,0,160,90

00:00:02.000 --> 00:00:04.000
sprite.jpg#xywh=160,0,160,90

1:00.500 --> 1:02.000
sprite.jpg#xywh=0,90,160,90

garbage --> 00:00:05.000
sprite.jpg#xywh=1,1,1,1
`;

test("a WebVTT thumbnails track parses to cues, skipping lines it does not understand", () => {
  const cues = parseSpriteVtt(VTT);
  expect(cues).toEqual([
    { startMs: 0, endMs: 2000, x: 0, y: 0, w: 160, h: 90 },
    { startMs: 2000, endMs: 4000, x: 160, y: 0, w: 160, h: 90 },
    { startMs: 60_500, endMs: 62_000, x: 0, y: 90, w: 160, h: 90 },
  ]);
  expect(parseSpriteVtt("")).toEqual([]);
  expect(parseSpriteVtt("﻿WEBVTT\r\n\r\n00:00:01.5 --> 00:00:02.0\r\nsprite.jpg#xywh=0,0,10,10\r\n")).toEqual([
    { startMs: 1500, endMs: 2000, x: 0, y: 0, w: 10, h: 10 },
  ]);
});

test("a cue is found by time, with the ends clamped", () => {
  const cues = parseSpriteVtt(VTT);
  expect(cueTile(cues, 0)?.x).toBe(0);
  expect(cueTile(cues, 2000)?.x).toBe(160);
  expect(cueTile(cues, 3999)?.x).toBe(160);
  expect(cueTile(cues, 99_000)?.y).toBe(90);
  expect(cueTile([], 0)).toBeUndefined();
  expect(cuesSheet(cues)).toEqual({ width: 320, height: 180 });
});

test("the sprite model answers for a grid at once and for a track once its cues are loaded", () => {
  const grid = spriteModel({ url: "s.jpg", grid: GRID }, 20_000, null);
  expect(grid?.at(0.5).tile).toEqual({ x: 0, y: 360, w: 100, h: 180 });
  expect(spriteModel({ url: "s.jpg", vttUrl: "s.vtt" }, 20_000, null)).toBeNull();
  const track = spriteModel({ url: "s.jpg", vttUrl: "s.vtt" }, 4000, parseSpriteVtt(VTT));
  expect(track?.at(0.75).tile).toMatchObject({ x: 160, y: 0 });
  expect(track?.at(0.75).sheet).toEqual({ width: 320, height: 180 });
});
