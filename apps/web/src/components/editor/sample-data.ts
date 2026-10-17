import { buildTimeMap } from "./lib/time-map";
import type { ReviewAnnotation, ReviewVersion, VideoItem } from "./types";

// A made-up 9:16 edit with three versions, so the review screen shows every comment state without a backend:
//   v1  hook, lat pulldown, finisher        12.0 s
//   v2  the agent drops the pulldown clip, trims the hook and adds a b-roll shot       10.5 s
//   v3  the agent speeds up the finisher    9.7 s

export const SAMPLE_ASSET_NAMES: Record<string, string> = {
  ast_hook: "Hook, gym floor",
  ast_pulldown: "Lat pulldown",
  ast_finisher: "Finisher set",
  ast_broll: "B-roll, chalk",
};

const hook = (source: [number, number]): VideoItem => ({
  id: "c1",
  asset_id: "ast_hook",
  at_ms: 0,
  source: { in_ms: source[0], out_ms: source[1] },
  speed: 1,
});

const v1Items: VideoItem[] = [
  hook([1000, 5000]),
  { id: "c2", asset_id: "ast_pulldown", at_ms: 4000, source: { in_ms: 0, out_ms: 4000 }, speed: 1 },
  { id: "c3", asset_id: "ast_finisher", at_ms: 8000, source: { in_ms: 2000, out_ms: 6000 }, speed: 1 },
];

const v2Items: VideoItem[] = [
  hook([1500, 5000]),
  { id: "c3", asset_id: "ast_finisher", at_ms: 3500, source: { in_ms: 2000, out_ms: 6000 }, speed: 1 },
  { id: "c4", asset_id: "ast_broll", at_ms: 7500, source: { in_ms: 500, out_ms: 3500 }, speed: 1 },
];

const v3Items: VideoItem[] = [
  hook([1500, 5000]),
  { id: "c3", asset_id: "ast_finisher", at_ms: 3500, source: { in_ms: 2000, out_ms: 6000 }, speed: 1.25 },
  { id: "c4", asset_id: "ast_broll", at_ms: 6700, source: { in_ms: 500, out_ms: 3500 }, speed: 1 },
];

export const SAMPLE_VERSIONS: ReviewVersion[] = [
  {
    version: 1,
    created_at: "2026-10-14T09:00:00Z",
    author_kind: "agent",
    summary: "First edit: hook, lat pulldown and finisher",
    output: { format: "9:16", fps: 30 },
    duration_ms: 12_000,
    items: v1Items,
    time_map: buildTimeMap(v1Items),
  },
  {
    version: 2,
    created_at: "2026-10-14T09:20:00Z",
    author_kind: "agent",
    summary: "Removed c2, trimmed c1 by 500 ms at the start, added b-roll c4",
    output: { format: "9:16", fps: 30 },
    duration_ms: 10_500,
    items: v2Items,
    time_map: buildTimeMap(v2Items),
  },
  {
    version: 3,
    created_at: "2026-10-15T08:30:00Z",
    author_kind: "agent",
    summary: "Sped up c3 to 1.25×",
    output: { format: "9:16", fps: 30 },
    duration_ms: 9700,
    items: v3Items,
    time_map: buildTimeMap(v3Items),
  },
];

const creator = { kind: "creator", name: "Sam" } as const;
const agent = { kind: "agent", name: "Editor agent" } as const;

export const SAMPLE_COMMENTS: ReviewAnnotation[] = [
  {
    id: "ann_pulldown",
    version: 1,
    start_ms: 4500,
    end_ms: 6500,
    kind: "comment",
    body: "This pulldown clip drags. Cut it.",
    status: "applied",
    applied_in_version: 2,
    author: creator,
    created_at: "2026-10-14T09:05:00Z",
  },
  {
    id: "ann_pulldown_reply",
    version: 2,
    start_ms: 4500,
    end_ms: 6500,
    kind: "comment",
    body: "Removed c2. The finisher now follows the hook directly.",
    status: "applied",
    applied_in_version: 2,
    author: agent,
    reply_to: "ann_pulldown",
    created_at: "2026-10-14T09:20:30Z",
  },
  {
    id: "ann_text",
    version: 1,
    start_ms: 9000,
    end_ms: 11_000,
    pin: { x: 0.5, y: 0.78 },
    kind: "comment",
    body: "Make this text bigger.",
    status: "applied",
    applied_in_version: 2,
    author: creator,
    created_at: "2026-10-14T09:06:00Z",
  },
  {
    id: "ann_text_reply",
    version: 2,
    start_ms: 9000,
    end_ms: 11_000,
    kind: "comment",
    body: "Raised the size of t1 from 64 to 88 and kept it inside the safe zone.",
    status: "applied",
    applied_in_version: 2,
    author: agent,
    reply_to: "ann_text",
    created_at: "2026-10-14T09:20:45Z",
  },
  {
    id: "ann_hook",
    version: 2,
    start_ms: 1200,
    end_ms: 1200,
    kind: "comment",
    body: "Start on the hook faster.",
    status: "open",
    author: creator,
    created_at: "2026-10-14T10:00:00Z",
  },
  {
    id: "ann_pace",
    version: 2,
    start_ms: 6000,
    end_ms: 7500,
    kind: "comment",
    body: "Give this rep more energy.",
    status: "applied",
    applied_in_version: 3,
    author: creator,
    created_at: "2026-10-14T10:05:00Z",
  },
  {
    id: "ann_broll",
    version: 2,
    start_ms: 8000,
    end_ms: 9500,
    kind: "comment",
    body: "The b-roll cuts in too early, show the chalk longer.",
    status: "open",
    author: creator,
    created_at: "2026-10-14T10:08:00Z",
  },
  {
    id: "ann_logo",
    version: 3,
    start_ms: 2600,
    end_ms: 2600,
    pin: { x: 0.5, y: 0.3 },
    kind: "comment",
    body: "Put the logo here.",
    status: "open",
    author: creator,
    created_at: "2026-10-15T09:00:00Z",
  },
];
