/** Canned API objects for tests, in the types of the contract. Each factory takes the fields a test cares about. */
import type { ApiAsset, Project, ProjectSummary, Render, Run, RunEvent, TimelineVersion, TimelineVersionSummary } from "@/lib/api/client";

const ID = "01jh6m00000000000000000000";
const typeId = (prefix: string, n: number) => `${prefix}_${ID.slice(0, 26 - String(n).length)}${n}`;

export const assetId = (n: number) => typeId("ast", n);
export const projectId = (n = 1) => typeId("prj", n);
export const runId = (n = 1) => typeId("run", n);
export const renderId = (n = 1) => typeId("ren", n);

const NOW = "2026-10-17T10:00:00Z";

export function apiAsset(n: number, over: Partial<ApiAsset> = {}): ApiAsset {
  return {
    id: assetId(n),
    kind: "video",
    status: "ready",
    filename: `clip-${n}.mp4`,
    content_type: "video/mp4",
    size_bytes: 1000,
    sha256: null,
    title: `Clip ${n}`,
    description: "",
    tags: [],
    failure_reason: null,
    ingest_progress: 100,
    ingest_stage: null,
    media: { duration_ms: 10_000 + n * 1000, width: 1080, height: 1920 } as ApiAsset["media"],
    derivatives: {
      poster: { url: `https://cdn.test/poster-${n}.jpg` },
      hover_preview: { url: `https://cdn.test/hover-${n}.mp4` },
    } as ApiAsset["derivatives"],
    derivative_urls_expire_at: "2026-10-17T11:00:00Z",
    created_at: NOW,
    updated_at: NOW,
    ...over,
  };
}

export function projectSummary(n = 1, over: Partial<ProjectSummary> = {}): ProjectSummary {
  return { id: projectId(n), title: `Project ${n}`, format: "9:16", status: "draft", asset_count: 2, current_timeline_version: null, created_at: NOW, updated_at: NOW, ...over };
}

export function versionSummary(version: number, over: Partial<TimelineVersionSummary> = {}): TimelineVersionSummary {
  return {
    id: typeId("tln", version),
    version,
    parent_version: version > 1 ? version - 1 : null,
    author_kind: "agent",
    author_id: runId(1),
    summary: [`Change ${version}`],
    created_at: NOW,
    ...over,
  };
}

/** A clip of the main track. */
export const clip = (id: string, asset: number, at: number, inMs: number, outMs: number, speed = 1) => ({
  id,
  asset_id: assetId(asset),
  at_ms: at,
  source: { in_ms: inMs, out_ms: outMs },
  speed,
});

export function timelineVersion(version: number, clips: ReturnType<typeof clip>[], over: Partial<TimelineVersionSummary> = {}): TimelineVersion {
  return {
    ...versionSummary(version, over),
    spec: {
      schema: "contentfabrique.timeline/v1",
      output: { format: "9:16", width: 1080, height: 1920, fps: 30, background: "#000000" },
      tracks: [{ id: "main", type: "video", items: clips }],
      markers: [],
    },
    ops: [],
    diff: [],
    issues: [],
  } as TimelineVersion;
}

export function project(n = 1, over: Partial<Project> = {}): Project {
  return {
    id: projectId(n),
    title: `Project ${n}`,
    format: "9:16",
    brief: "Open on the pose",
    status: "review",
    asset_count: 2,
    current_timeline_version: null,
    current_version: null,
    assets: [
      { asset_id: assetId(1), position: 0, title: "Row 1", kind: "video", status: "ready", duration_ms: 11_000, available: true },
      { asset_id: assetId(2), position: 1, title: "Row 2", kind: "video", status: "ready", duration_ms: 12_000, available: true },
    ],
    created_at: NOW,
    updated_at: NOW,
    ...over,
  } as Project;
}

export function event(seq: number, type: RunEvent["type"], text: string, over: Partial<RunEvent> = {}): RunEvent {
  return { seq, type, text, created_at: NOW, ...over };
}

export function run(over: Partial<Run> = {}): Run {
  return {
    id: runId(1),
    agent: "editor",
    project_id: projectId(1),
    status: "queued",
    failure_reason: null,
    question: null,
    summary: null,
    timeline_version: null,
    routing_profile: "dev-cheap",
    model: "claude-haiku",
    effort: "low",
    billed_to: "platform",
    cost_micros: 0,
    budget_micros: 5_000_000,
    tokens: { input: 0, cache_write: 0, cache_read: 0, output: 0, cache_hit_rate: null },
    tool_calls: 0,
    budget_tool_calls: 60,
    active_ms: 0,
    budget_wall_clock_ms: 600_000,
    created_at: NOW,
    started_at: null,
    finished_at: null,
    events: [],
    last_seq: 0,
    ...over,
  } as Run;
}

export function render(over: Partial<Render> = {}): Render {
  return {
    id: renderId(1),
    project_id: projectId(1),
    timeline_version: 1,
    profile: "preview",
    status: "queued",
    progress: 0,
    stage: null,
    duration_ms: null,
    size_bytes: null,
    contact_sheet: null,
    sound_cues: [],
    files: null,
    urls_expires_at: null,
    failure_reason: null,
    requested_by: { kind: "user", id: "usr_1" },
    created_at: NOW,
    started_at: null,
    finished_at: null,
    ...over,
  };
}

/** A ready render with presigned URLs. */
export function readyRender(over: Partial<Render> = {}): Render {
  return render({
    status: "ready",
    progress: 100,
    duration_ms: 12_000,
    files: {
      mp4: { url: "https://files.test/preview.mp4", content_type: "video/mp4", size_bytes: 1000 },
      contact_sheet: { url: "https://files.test/sheet.jpg", content_type: "image/jpeg", size_bytes: 100 },
      time_map: { url: "https://files.test/time-map.json", content_type: "application/json", size_bytes: 100 },
    },
    urls_expires_at: "2099-01-01T00:00:00Z",
    ...over,
  });
}
