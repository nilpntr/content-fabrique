import type { CreatorStyle, StyleSettings } from "@/lib/api/client";
import { withMySettings } from "@/lib/style-words";
import { annotation, apiAsset, assetId, callIdOf, emptyStyle, event, learntStyle, project, projectId, projectSummary, readyRender, renderId, run, runId, styleReference, timelineVersion, v2Artifacts, v2Run, v2Spec, versionSummary } from "@/test-fixtures";

/** The scenario of a page, from its address: `?credits=ok|low|empty|none&run=done|working|failed|question&runcredits=charged|revision|refunded|none&artifacts=api|none&video=<url>&posters=<url with {n}>&projects=some|none&style=ready|learning|empty`. */
export interface Scenario {
  credits: "none" | "ok" | "low" | "empty";
  run: "done" | "working" | "failed" | "question";
  /** What the run's own credits say, for a run that finished: charged for its first edit, charged for a revision, refunded, or nothing (released). */
  runCredits: "charged" | "revision" | "refunded" | "none";
  /** `none`: the artifacts endpoint is not there (an older server), so the feed is made from the event summaries. */
  artifacts: "api" | "none";
  /** An MP4 to play as the preview (the viewer has none of its own). */
  video?: string;
  /** Posters for the clips instead of gradients: an address with `{n}` for the clip's number. */
  posters?: string;
  /** `none`: a brand-new workspace with no projects yet. */
  projects: "some" | "none";
  /** The creator's style: learnt from five videos, being learnt, or nothing yet. */
  style: "ready" | "learning" | "empty";
}

export function scenarioOf(search: URLSearchParams): Scenario {
  const pick = <T extends string>(key: string, allowed: readonly T[], fallback: T): T => {
    const value = search.get(key);
    return allowed.includes(value as T) ? (value as T) : fallback;
  };
  return {
    credits: pick("credits", ["none", "ok", "low", "empty"], "none"),
    run: pick("run", ["done", "working", "failed", "question"], "done"),
    runCredits: pick("runcredits", ["charged", "revision", "refunded", "none"], "charged"),
    artifacts: pick("artifacts", ["api", "none"], "api"),
    video: search.get("video") ?? undefined,
    posters: search.get("posters") ?? undefined,
    projects: pick("projects", ["some", "none"], "some"),
    style: pick("style", ["ready", "learning", "empty"], "ready"),
  };
}

/** A gradient poster as a data address, so the viewer needs no images. */
function poster(n: number): string {
  const hue = (n * 67) % 360;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="360" height="450"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${hue} 45% 42%)"/><stop offset="1" stop-color="hsl(${(hue + 50) % 360} 50% 22%)"/></linearGradient></defs><rect width="360" height="450" fill="url(#g)"/></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const CLIPS = [
  { n: 1, title: "Heaviest lat pulldown", description: "A lifter in a grey tank top pulls a heavy lat pulldown to a hard lockout.", tags: ["lat pulldown", "back day", "lockout"] },
  { n: 2, title: "Seated cable row", description: "Controlled seated rows with a pause at the squeeze.", tags: ["row", "cable", "back day"] },
  { n: 3, title: "Back pose, held", description: "A held back double biceps pose in warm gym light.", tags: ["posing", "back"] },
  { n: 4, title: "Chalking up", description: "Hands in chalk before the last set.", tags: ["chalk", "detail"] },
  { n: 5, title: "Treadmill warm-up", description: "", tags: [] },
];

const clipAsset = (clip: (typeof CLIPS)[number], posters?: string) =>
  apiAsset(clip.n, {
    title: clip.title,
    description: clip.description,
    tags: clip.tags,
    quick_look_status: clip.description ? "succeeded" : "pending",
    derivatives: { poster: { url: posters && clip.n <= 3 ? posters.replace("{n}", String(clip.n)) : poster(clip.n) } } as never,
  });

const NAMES: Record<number, string> = { 1: "Heaviest lat pulldown", 2: "Seated cable row", 3: "Back pose, held" };

function fixtureProject(n = 1) {
  if (n === 3) {
    return project(3, { title: "Leg day", brief: "", status: "editing", asset_count: 1, assets: [{ asset_id: assetId(2), position: 0, title: "Seated cable row", kind: "video", status: "ready", duration_ms: 12_000, available: true }] } as never);
  }
  return project(1, {
    title: "Gino's back day",
    brief: "Open on the heaviest pulldown, cut to the beat, end on the pose.",
    asset_count: 3,
    current_timeline_version: 2,
    assets: [1, 2, 3].map((n, position) => ({ asset_id: assetId(n), position, title: NAMES[n] ?? `Clip ${n}`, kind: "video", status: "ready", duration_ms: 12_000, available: true })),
  } as never);
}

function creditsFor(kind: Scenario["credits"]) {
  if (kind === "none") return undefined;
  const periodEnd = new Date(Date.now() + 14 * 86_400_000).toISOString();
  if (kind === "ok") return { available: 42, allowance: 60, period_end: periodEnd, low: false, top_ups: [{ credits: 20, expires_at: new Date(Date.now() + 200 * 86_400_000).toISOString() }] };
  if (kind === "low") return { available: 6, allowance: 60, period_end: periodEnd, low: true, top_ups: [] };
  return { available: 0, allowance: 60, period_end: periodEnd, low: true, top_ups: [] };
}

/** The events of the run in a scenario, up to the one it has reached. */
const LAST_SEQ: Record<Scenario["run"], number> = { done: 99, working: 26, question: 16, failed: 23 };

function runFor(kind: Scenario["run"], runCredits: Scenario["runCredits"]) {
  if (kind === "done") {
    const credits = runCredits === "none" ? null : runCredits === "revision" ? { state: "charged" as const, action: "edit.revision" as const, credits: 2 } : runCredits === "refunded" ? { state: "refunded" as const, action: "edit.first" as const, credits: 6 } : undefined;
    return runCredits === "charged" ? v2Run() : v2Run({ credits });
  }
  const finished = v2Run().events;
  if (kind === "working") {
    // Up to the second render: the plan, one round of checks, the brightness match, and the preview that is being made.
    const events = finished.filter((e) => e.seq <= 26).concat(event(27, "tool_call", "render_preview {}", { tool: "render_preview", call_id: callIdOf(27) }));
    return run({ ...v2Run({ credits: { state: "held", action: "edit.first", credits: 6 } }), status: "running", summary: null, timeline_version: null, cost_micros: 148_000, active_ms: 61_000, tool_calls: 11, events, last_seq: 27 });
  }
  if (kind === "question") {
    return run({ ...v2Run({ credits: { state: "held", action: "edit.first", credits: 6 } }), status: "waiting_input", question: "The back pose clip is much brighter than the rest. Should I even it out, or keep it as it is?", summary: null, timeline_version: null, events: finished.filter((e) => e.seq <= 16), last_seq: 16 });
  }
  return run({ ...v2Run({ credits: null }), status: "failed", failure_reason: "budget", summary: "I made v1 and previewed it, but ran out of steps before the second check.", events: [...finished.filter((e) => e.seq <= 23), event(24, "status", "failed reason=budget limit=tool_calls")], last_seq: 24 });
}

/** The style of a scenario, with gradient stills for the reference videos. */
function styleFor(kind: Scenario["style"]): CreatorStyle {
  if (kind === "empty") return emptyStyle();
  const learnt = learntStyle();
  const refs = learnt.references.map((ref, i) => ({ ...ref, poster_url: poster(i + 7) }));
  if (kind === "ready") return { ...learnt, references: refs };
  return {
    ...emptyStyle(),
    status: "learning",
    progress: { done: 2, total: 4 },
    references: [
      refs[0] ?? styleReference(1),
      refs[1] ?? styleReference(2),
      { ...(refs[2] ?? styleReference(3)), status: "analysing" },
      { ...styleReference(4), status: "processing", progress: 45, poster_url: null },
      { ...styleReference(5), name: "old-vlog.mov", status: "failed", failure_reason: "too_long", poster_url: null },
    ],
  };
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const notFound = () => json({ error: { code: "not_found", message: "not found" } }, 404);

/** Answers the API's paths with the fixtures of the tests, for the page's scenario. Anything else is a 404. */
export function installFakeApi(scenario: Scenario): void {
  const credits = creditsFor(scenario.credits);
  const me = {
    user: { id: "usr_01test", email: "ada@example.com" },
    workspace: { id: "wks_01test", name: "Fit Studio", role: "owner" },
    entitlements: { plan: "creator", status: "active", active: true, current_period_end: null },
    ...(credits ? { credits } : {}),
  };
  const versions = [versionSummary(2, { summary: ["Evened out the brightness of c4 and c5"], author_id: runId(1) }), versionSummary(1, { summary: ["First edit: hook, rows and the pose"], author_id: runId(1) })];
  const render = readyRender({ project_id: projectId(1), timeline_version: 2, duration_ms: 10_900, files: { mp4: { url: scenario.video ?? "https://files.invalid/preview.mp4", content_type: "video/mp4", size_bytes: 1000 }, contact_sheet: null, time_map: { url: "https://files.invalid/time-map.json", content_type: "application/json", size_bytes: 10 } } as never });

  const finals: ReturnType<typeof readyRender>[] = [];
  let style = styleFor(scenario.style);
  const original = window.fetch.bind(window);
  window.fetch = async (input, init) => {
    const request = typeof input === "string" || input instanceof URL ? new Request(input, init) : input;
    const url = new URL(request.url);
    if (url.host !== "api.fixtures") return url.host.endsWith(".invalid") ? notFound() : original(input, init);
    const path = url.pathname;
    const method = request.method;
    if (method === "GET") {
      if (path === "/v1/me") return json(me);
      if (path === "/v1/assets") return json({ items: CLIPS.map((clip) => clipAsset(clip, scenario.posters)), next_cursor: null });
      if (path === "/v1/projects" && scenario.projects === "none") return json({ items: [], next_cursor: null });
      if (path === "/v1/projects") {
        const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();
        return json({
          items: [
            projectSummary(1, { title: "Gino's back day", status: "review", asset_count: 3, current_timeline_version: 2, updated_at: hoursAgo(2) }),
            projectSummary(3, { title: "Leg day", status: "editing", asset_count: 1, updated_at: hoursAgo(0.2) }),
            projectSummary(2, { title: "Pose practice", status: "draft", asset_count: 0, updated_at: hoursAgo(30) }),
          ],
          next_cursor: null,
        });
      }
      if (path === `/v1/projects/${projectId(1)}`) return json(fixtureProject());
      if (path === `/v1/projects/${projectId(3)}`) return json(fixtureProject(3));
      if (path === `/v1/projects/${projectId(1)}/timelines`) return json({ items: versions, next_before: null });
      const version = /\/timelines\/(\d+)$/.exec(path)?.[1];
      if (version) {
        const full = timelineVersion(Number(version), [], { summary: versions.find((v) => v.version === Number(version))?.summary ?? [] });
        const spec = Number(version) === 2 ? v2Spec() : (full as { spec: unknown }).spec;
        return json({ ...full, spec: Number(version) === 1 ? withoutEffects(v2Spec()) : spec });
      }
      if (path === `/v1/projects/${projectId(1)}/renders`) return json({ items: url.searchParams.get("profile") === "final" ? finals : [render] });
      const finalId = finals.find((candidate) => path === `/v1/renders/${candidate.id}`);
      if (finalId) return json(finalId);
      if (path === `/v1/renders/${renderId(1)}`) return json(render);
      if (path === `/v1/runs/${runId(1)}`) return json(runFor(scenario.run, scenario.runCredits));
      if (path === `/v1/runs/${runId(1)}/artifacts`) {
        if (scenario.artifacts === "none") return notFound();
        const after = Number(url.searchParams.get("after_seq") ?? 0);
        return json({ artifacts: v2Artifacts().artifacts.filter((artifact) => artifact.seq > after && artifact.seq <= LAST_SEQ[scenario.run]), next_after_seq: null });
      }
      if (path === "/v1/annotations") return json({ items: [annotation(1, { timeline_version: 2, start_ms: 2900, end_ms: 4400, body: "The ramp on the cable row is great. Hold the slow part a beat longer." }), annotation(2, { timeline_version: 2, start_ms: 6200, end_ms: 6200, body: "Title here, please." })], next_cursor: null });
      if (path === "/v1/library/summary") return json({ total: CLIPS.length });
      if (path === "/v1/style") return json(style);
    }
    // The style: settings are kept for the page's life, a removed reference goes, learning again starts over.
    if (method === "PUT" && path === "/v1/style") {
      const body = (await request.json()) as { settings: StyleSettings };
      style = withMySettings(style, body.settings);
      return json(style);
    }
    if (method === "POST" && path === "/v1/style/relearn") {
      style = { ...style, status: "learning", progress: { done: 0, total: style.references.length } };
      return json(style, 202);
    }
    if (method === "DELETE" && path.startsWith("/v1/style/references/")) {
      const id = path.slice("/v1/style/references/".length);
      style = { ...style, references: style.references.filter((ref) => ref.asset_id !== id) };
      return new Response(null, { status: 204 });
    }
    if (method === "DELETE" && path === "/v1/style") {
      style = emptyStyle();
      return new Response(null, { status: 204 });
    }
    // A download: the final video is ready at once (the viewer has no renderer).
    if (method === "POST" && path === `/v1/projects/${projectId(1)}/renders`) {
      const body = (await request.json()) as { version: number; profile: string };
      const made = readyRender({ id: renderId(50 + body.version), project_id: projectId(1), timeline_version: body.version, profile: "final", files: { mp4: { url: scenario.video ?? "https://files.invalid/final.mp4", content_type: "video/mp4", size_bytes: 1000 }, contact_sheet: null, time_map: { url: "https://files.invalid/time-map.json", content_type: "application/json", size_bytes: 10 } } as never });
      finals.push(made);
      return json(made, 201);
    }
    // Starting an edit with no credits left is refused, as the API will (402 credits_exhausted).
    if (method === "POST" && path === `/v1/projects/${projectId(1)}/edit` && scenario.credits === "empty") return json({ error: { code: "credits_exhausted", message: "not enough credits" } }, 402);
    return notFound();
  };
}

/** Version 1 of the viewer's project: the same clips, cut hard, with none of the effects. */
function withoutEffects(spec: unknown): unknown {
  const copy = JSON.parse(JSON.stringify(spec)) as { tracks: { type: string; items: Record<string, unknown>[] }[]; markers?: unknown[] };
  for (const track of copy.tracks) {
    if (track.type === "video") {
      track.items = track.items.map((item, i) => {
        const { motion: _m, look: _l, speed_ramp: _r, transition_in: _t, audio: _a, why: _w, ...rest } = item;
        return { ...rest, speed: 1, at_ms: i * 1800, source: { in_ms: 0, out_ms: 1800 } };
      });
    } else {
      track.items = [];
    }
  }
  copy.markers = [];
  return copy;
}

export const FIXTURE_PROJECT_PATH = `/projects/${projectId(1)}?run=${runId(1)}`;
