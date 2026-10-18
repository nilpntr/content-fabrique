/** Canned API objects for tests, in the types of the contract. Each factory takes the fields a test cares about. */
import type { Annotation, ApiAsset, CreatorStyle, Project, ProjectSummary, Render, Run, RunArtifact, RunArtifacts, RunEvent, StyleReference, TimelineVersion, TimelineVersionSummary } from "@/lib/api/client";

const ID = "01jh6m00000000000000000000";
const typeId = (prefix: string, n: number) => `${prefix}_${ID.slice(0, 26 - String(n).length)}${n}`;

export const assetId = (n: number) => typeId("ast", n);
export const projectId = (n = 1) => typeId("prj", n);
export const runId = (n = 1) => typeId("run", n);
export const renderId = (n = 1) => typeId("ren", n);
export const annotationId = (n = 1) => typeId("ann", n);

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
    transcript_status: "none",
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
    width: null,
    height: null,
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

/** A comment on version 3 of project 1, by the signed-in user (`usr_01test`, see ME in test-clerk). */
export function annotation(n = 1, over: Partial<Annotation> = {}): Annotation {
  return {
    id: annotationId(n),
    target_type: "timeline_version",
    target_id: typeId("tl", 3),
    project_id: projectId(1),
    timeline_version: 3,
    start_ms: 1000,
    end_ms: 2000,
    pin: null,
    kind: "comment",
    body: `Comment ${n}`,
    status: "open",
    parent_id: null,
    author: { kind: "user", id: "usr_01test" },
    applied_in_run_id: null,
    addressed_in_version: null,
    created_at: `2026-10-17T10:0${n}:00Z`,
    updated_at: NOW,
    ...over,
  };
}

// ---- An editor v2 run ------------------------------------------------------------------------------------------------------
// What `GET /v1/runs/{id}` returns for a run of the v2 editor (docs/design/edit-style-v1.md sections 6 and 7): the plan, two rounds
// of self-review with a brightness match between them, the finish. The texts are written as the server writes them (the tools'
// answers) and cut as the server cuts them (one line, 400 bytes, "…"), so the readers are tested on what the API really carries.

/** The server's summary of an event text: white space collapsed, at most 400 bytes, an ellipsis when it cut (runtime/summary.go). */
export function summarize(text: string, max = 400): string {
  const line = text.split(/\s+/).filter(Boolean).join(" ");
  if (new TextEncoder().encode(line).length <= max) return line;
  let out = "";
  for (const char of line) {
    if (new TextEncoder().encode(out + char).length > max) break;
    out += char;
  }
  return `${out}…`;
}

/** The `data` of the plan's `creative_plan` artifact, as the design note documents it. */
export const PLAN_PAYLOAD = {
  version: 1,
  content_type: "workout",
  story: "Gino's back day in 25 seconds: three pulls, one posing finish.",
  hook: "The heaviest lat pulldown lockout, punched in on the first beat.",
  arc: "Hook on the lockout, build through the rows, peak on the pulldown, end on the pose.",
  ending: "Hold the pose and loop back to the lockout.",
  playbook: "workout",
  playbook_version: "workout.v1",
  target_length_s: 25,
  tempo_bpm: 124,
  pace: "building",
  beat_ms: 484,
  estimated_length_ms: 24_200,
  shots: [
    { role: "hook", what: "Heaviest lat pulldown lockout", asset_id: assetId(1), beats: 3, effect: "punch_in", length_ms: 1452 },
    { role: "build", what: "Seated row, full rep", asset_id: assetId(2), beats: 4, effect: "none", length_ms: 1936 },
    { role: "build", what: "Cable row, squeeze", asset_id: assetId(3), beats: 4, effect: "speed_ramp", length_ms: 1936 },
    { role: "peak", what: "Pulldown, top of the rep", asset_id: assetId(1), beats: 2, effect: "whip", length_ms: 968 },
    { role: "detail", what: "Chalked hands", asset_id: assetId(2), beats: 6, effect: "none", length_ms: 2904 },
    { role: "ending", what: "Back pose, held", asset_id: assetId(3), beats: 8, effect: "flash", length_ms: 3872 },
  ],
  prompt_version: "editor.v2.1",
};

/** The `data` of the two `edit_check` artifacts: 12 of 13 pass, then 13 of 13 after the brightness match. */
export const CHECKS_ROUND_1 = {
  record_version: 1,
  version: 1,
  content_type: "workout",
  target_length_s: 25,
  checks: [
    { id: "hook_length", status: "pass", detail: "the first shot is 1.47 s long (at most 2.00 s)" },
    { id: "hook_title", status: "pass", detail: "a text starts at 0.30 s" },
    { id: "shot_pacing", status: "pass", detail: "the median shot is 2.40 s (at most 3.00 s)" },
    { id: "shot_variety", status: "pass", detail: "no more than two shots in a row have the same length" },
    { id: "pace_builds", status: "pass", detail: "the second half's median shot is 12 % shorter than the first half's" },
    { id: "rep_boundaries", status: "pass", detail: "6 of 6 shot edges are on a rep boundary" },
    { id: "dead_time", status: "pass", detail: "2 % of the screen time is dead time" },
    { id: "effect_density", status: "pass", detail: "0.40 effects per cut" },
    { id: "text_safe_zone", status: "pass", detail: "all text is inside the safe zones" },
    { id: "duration", status: "pass", detail: "24.2 s, the plan said 25 s" },
    { id: "brightness_jump", status: "fail", detail: "the cut from c3 to c4 jumps 0.31 in brightness: call match_exposure" },
    { id: "crushed", status: "pass", detail: "every shot keeps at least 70 % of its source's brightness" },
    { id: "beat_sync", status: "pass", detail: "5 of 5 cuts are on a beat (within 2 frames)" },
  ],
  passed: 12,
  failed: 1,
  skipped: 0,
  nth: 1,
};
export const CHECKS_ROUND_2 = {
  ...CHECKS_ROUND_1,
  version: 2,
  checks: CHECKS_ROUND_1.checks.map((check) => (check.id === "brightness_jump" ? { id: check.id, status: "pass", detail: "the largest jump at a cut is 0.09 in brightness" } : check)),
  passed: 13,
  failed: 0,
  nth: 2,
};

const PLAN_CALL = `plan_edit ${JSON.stringify({
  content_type: PLAN_PAYLOAD.content_type,
  story: PLAN_PAYLOAD.story,
  hook: PLAN_PAYLOAD.hook,
  arc: PLAN_PAYLOAD.arc,
  ending: PLAN_PAYLOAD.ending,
  playbook: PLAN_PAYLOAD.playbook,
  target_length_s: PLAN_PAYLOAD.target_length_s,
  tempo_bpm: PLAN_PAYLOAD.tempo_bpm,
  pace: PLAN_PAYLOAD.pace,
  shots: PLAN_PAYLOAD.shots.map(({ role, what, asset_id, beats, effect }) => ({ role, what, asset_id, beats, effect })),
})}`;

const PLAN_RESULT = [
  "Plan saved (it is shown to the creator). Content type workout, playbook workout.v1.",
  'Tempo: 124 BPM, one beat is 484 ms (2 beats 0.97 s, 4 beats 1.94 s, 8 beats 3.87 s). Write the grid as {"bpm":124,"first_beat_ms":0} in snap_cuts_to_beats with write_markers true.',
  "Shot list, 6 shots, 24.2 s in all (target 25 s):",
  "1. hook: 3 beats = 1.45 s, punch_in",
  "2. build: 4 beats = 1.94 s",
  "3. build: 4 beats = 1.94 s, speed_ramp",
  "4. peak: 2 beats = 0.97 s, whip",
  "5. detail: 6 beats = 2.90 s",
  "6. ending: 8 beats = 3.87 s, flash",
  "",
  "The playbook for this kind of video. Follow it; it is the craft of the genre, and check_edit will measure your edit against it.",
].join("\n");

/** The text of a `check_edit` answer for a round, as `Report.Text` writes it. */
export function checkText(round: typeof CHECKS_ROUND_1): string {
  const total = round.passed + round.failed;
  const lines = [`check_edit for version ${round.version} (${round.content_type}, target ${round.target_length_s} s): ${round.passed} of ${total} checks pass, ${round.failed} fail, ${round.skipped} do not apply.`];
  for (const check of round.checks) lines.push(`${check.status === "pass" ? "PASS" : check.status === "fail" ? "FAIL" : "n/a "} ${check.id}: ${check.detail}`);
  return lines.join("\n");
}

const MATCH_RESULT = [
  "Matched the brightness of 2 clips toward p90 luma 0.20 (the median of the timeline's shots; only the brighter ones are darkened, by at most 0.7 stops, and a clip is left alone under 0.15 stops):",
  "- c4: p90 0.37, exposure_ev 0.70",
  "- c5: p90 0.31, exposure_ev 0.40",
  "Render a preview and check_edit again: it measures the picture.",
].join("\n");

export const V2_SUMMARY = [
  "A 24 second vertical cut of Gino's back day, cut to 124 BPM.",
  "",
  "- Opens on the heaviest pulldown lockout with a punch-in on the first beat.",
  "- Rows build in rising energy, with a speed ramp on the cable row and a whip into the peak.",
  "- The posing clip was darker than the gym clips, so I evened out its brightness; the second check passed 13 of 13.",
].join("\n");

/** The model's id of the tool call that is event `seq`; its result and its artifact carry the same one. */
export const callIdOf = (seq: number): string => `toolu_${String(seq).padStart(4, "0")}`;
const call = (seq: number, tool: string, input: string) => event(seq, "tool_call", summarize(`${tool} ${input}`), { tool, call_id: callIdOf(seq) });
/** A result: the call it answers is the event before it (`seq - 1`). */
const answer = (seq: number, tool: string, text: string, over: Partial<RunEvent> = {}) => event(seq, "tool_result", summarize(text), { tool, call_id: callIdOf(seq - 1), ...over });
const artifactEvent = (seq: number, kind: string, callSeq: number) => event(seq, "artifact", kind, { kind, call_id: callIdOf(callSeq) });

/**
 * The events of a finished v2 run, oldest first (see the header of this section). They carry `call_id` and `kind`;
 * `{ legacy: true }` is the run as it was before the API had them (paired by order).
 */
export function v2RunEvents({ legacy = false }: { legacy?: boolean } = {}): RunEvent[] {
  const events = v2RunEventsWithIds();
  return legacy ? events.map(({ call_id: _call, kind: _kind, ...rest }) => rest) : events;
}

function v2RunEventsWithIds(): RunEvent[] {
  const view = (seq: number, n: number) => [call(seq, "view_clip", `{"asset_id":"${assetId(n)}"}`), answer(seq + 1, "view_clip", "[1 images]")];
  return [
    event(1, "status", "running"),
    call(2, "get_brief", "{}"),
    answer(3, "get_brief", "The creator's instructions: open on the heaviest pulldown."),
    call(4, "list_footage", "{}"),
    answer(5, "list_footage", "3 clips"),
    call(6, "find_moments", '{"query":"heaviest lockout"}'),
    answer(7, "find_moments", "4 moments"),
    ...view(8, 1),
    ...view(10, 2),
    ...view(12, 3),
    call(14, "plan_edit", PLAN_CALL.slice("plan_edit ".length)),
    answer(15, "plan_edit", PLAN_RESULT),
    artifactEvent(16, "creative_plan", 14),
    call(17, "edit_timeline", '{"base_version":0,"ops":[{"op":"add_clip"}]}'),
    answer(18, "edit_timeline", "v1 made"),
    call(19, "render_preview", "{}"),
    answer(20, "render_preview", "preview of v1 is ready"),
    call(21, "check_edit", "{}"),
    answer(22, "check_edit", checkText(CHECKS_ROUND_1)),
    artifactEvent(23, "edit_check", 21),
    call(24, "match_exposure", "{}"),
    answer(25, "match_exposure", MATCH_RESULT),
    call(26, "render_preview", "{}"),
    answer(27, "render_preview", "preview of v2 is ready"),
    call(28, "check_edit", "{}"),
    answer(29, "check_edit", checkText(CHECKS_ROUND_2)),
    artifactEvent(30, "edit_check", 28),
    call(31, "finish", JSON.stringify({ summary: V2_SUMMARY, no_edit_needed: false })),
    answer(32, "finish", V2_SUMMARY),
    event(33, "status", "succeeded"),
  ];
}

const stored = (seq: number, kind: string, callSeq: number | null, data: unknown): RunArtifact => ({
  seq,
  kind,
  call_id: callSeq === null ? null : callIdOf(callSeq),
  data: data as RunArtifact["data"],
  created_at: NOW,
});

/** What `GET /v1/runs/{id}/artifacts` returns for the v2 run: the whole plan and both rounds of checks (the artifact events are the ones at seq 16, 23 and 30 of `v2RunEvents`). */
export function v2Artifacts(): RunArtifacts {
  return {
    artifacts: [
      stored(16, "creative_plan", 14, PLAN_PAYLOAD),
      stored(23, "edit_check", 21, CHECKS_ROUND_1),
      stored(30, "edit_check", 28, CHECKS_ROUND_2),
    ],
    next_after_seq: null,
  };
}

/** A finished v2 run with its plan, two rounds of checks and a finish. */
export function v2Run(over: Partial<Run> = {}): Run {
  const events = v2RunEvents();
  return run({
    status: "succeeded",
    model: "claude-opus-5-5",
    cost_micros: 224_000,
    active_ms: 98_000,
    tool_calls: 16,
    summary: V2_SUMMARY,
    timeline_version: 2,
    credits: { state: "charged", action: "edit.first", credits: 6 },
    events,
    last_seq: events.length,
    ...over,
  });
}

/** The spec of an edit with a motion preset, a speed ramp, five kinds of transition, sound effects, a look, the three sound modes and a beat grid. */
export function v2Spec(): unknown {
  const items = [
    { id: "c1", asset_id: assetId(1), at_ms: 0, source: { in_ms: 1000, out_ms: 2450 }, speed: 1, motion: { preset: "punch_in", at_ms: 100, strength: 0.15 }, look: { preset: "gym_moody" }, audio: { mode: "bed" }, why: "The heaviest lockout of the set, so the video opens on its best frame." },
    { id: "c2", asset_id: assetId(2), at_ms: 1200, source: { in_ms: 0, out_ms: 2000 }, speed: 1, transition_in: { type: "whip_left", duration_ms: 250 }, look: { preset: "gym_moody" }, audio: { mode: "bed" } },
    { id: "c3", asset_id: assetId(3), at_ms: 2900, source: { in_ms: 500, out_ms: 2500 }, speed_ramp: [{ at_ms: 0, speed: 2 }, { at_ms: 2000, speed: 0.5 }], transition_in: { type: "zoom_through", duration_ms: 300 }, look: { preset: "gym_moody" }, audio: { mode: "bed" }, why: "Fast into slow at the peak of the contraction." },
    { id: "c4", asset_id: assetId(1), at_ms: 4598, source: { in_ms: 3000, out_ms: 5000 }, speed: 1, motion: { preset: "shake", at_ms: 0 }, transition_in: { type: "flash", duration_ms: 150 }, look: { preset: "gym_moody", match: true }, audio: { mode: "full", gain_db: -3 } },
    { id: "c5", asset_id: assetId(2), at_ms: 6198, source: { in_ms: 2000, out_ms: 4500 }, speed: 1, motion: { preset: "push_in" }, transition_in: { type: "crossfade", duration_ms: 400 }, audio: { mode: "mute" } },
    { id: "c6", asset_id: assetId(3), at_ms: 8398, source: { in_ms: 0, out_ms: 2500 }, speed: 1, motion: { preset: "pull_back" }, transition_in: { type: "dip_to_black", duration_ms: 300 }, audio: { mode: "mute" } },
  ];
  const beats = Array.from({ length: 23 }, (_, i) => ({ at_ms: Math.round(i * 484), kind: i % 4 === 0 ? "downbeat" : "beat", strength: 1 }));
  return {
    schema: "contentfabrique.timeline/v1",
    output: { format: "9:16", width: 1080, height: 1920, fps: 30, background: "#000000" },
    tracks: [
      { id: "main", type: "video", items },
      {
        id: "sound",
        type: "audio",
        items: [
          { id: "s1", kind: "sfx", sfx: "riser", variant: "tension", at_ms: 0, export: "licensed" },
          { id: "s2", kind: "sfx", sfx: "impact", variant: "punch", at_ms: 100, export: "licensed", gain_db: 2 },
          { id: "s3", kind: "sfx", sfx: "whoosh", variant: "fast", at_ms: 950, export: "licensed" },
          { id: "s4", kind: "sfx", sfx: "whoosh", variant: "heavy", at_ms: 2650, export: "licensed" },
          { id: "s5", kind: "sfx", sfx: "impact", variant: "crack", at_ms: 4598, export: "licensed" },
          { id: "s6", kind: "sfx", sfx: "sub_drop", variant: "boom", at_ms: 4608, export: "licensed" },
          { id: "s7", kind: "sfx", sfx: "click", at_ms: 8000, export: "licensed" },
          { id: "s8", kind: "trend_sound", trend_id: "trd_01jh6m00000000000000000000", at_ms: 0, source_offset_ms: 0, export: "cue_only" },
        ],
      },
    ],
    markers: beats,
  };
}

// ── The creator's style ─────────────────────────────────────────────────────

/** A style reference video. */
export function styleReference(n: number, over: Partial<StyleReference> = {}): StyleReference {
  return { asset_id: assetId(100 + n), name: `video-${n}.mp4`, status: "ready", failure_reason: null, progress: 100, duration_ms: 20_000, poster_url: null, created_at: NOW, ...over };
}

/** A style with nothing learnt: the defaults. */
export function emptyStyle(over: Partial<CreatorStyle> = {}): CreatorStyle {
  return {
    status: "empty",
    progress: { done: 0, total: 0 },
    summary: null,
    measures: null,
    settings: {},
    effective: {
      vibe: "lively",
      transitions: "subtle",
      text_animation: "on",
      camera_motion: "subtle",
      title_colour: null,
      title_position: "auto",
      title_case: "as_typed",
      music: "always",
      pace: "like_mine",
      chapter_titles: "on",
      title_template: null,
      ending_hold: "long",
      cut_timing: "full_reps",
      angle_switches: "on",
      sources: {
        vibe: "default",
        transitions: "vibe",
        text_animation: "vibe",
        camera_motion: "vibe",
        title_colour: "default",
        title_position: "default",
        title_case: "default",
        music: "default",
        pace: "default",
        chapter_titles: "default",
        title_template: "default",
        ending_hold: "default",
        cut_timing: "default",
        angle_switches: "default",
      },
    },
    references: [],
    limits: { max_references: 10, max_size_bytes: 1_073_741_824, max_duration_ms: 180_000 },
    learned_at: null,
    ...over,
  };
}

/** A learnt style: five videos, the AI's sentence, the measures, and one setting of the creator's own (transitions: lots). */
export function learntStyle(over: Partial<CreatorStyle> = {}): CreatorStyle {
  const base = emptyStyle();
  return {
    ...base,
    status: "ready",
    progress: { done: 5, total: 5 },
    summary: {
      text: "Handheld gym shots that follow you, music on every video with a lift around 3 s, cream all-caps titles above your head, mostly 2-second shots, cuts mid-rep, numbered chapter titles and a long hold at the end.",
      suggested: { vibe: "lively", transitions: "subtle", text_animation: "on", camera_motion: "subtle", title_colour: "#FFF4D6", title_position: "top", title_case: "upper", music: "always", pace: "like_mine", chapter_titles: "on", ending_hold: "long", cut_timing: "mid_rep", angle_switches: "on" },
      references: 5,
      prompt_version: "stylesummary.v1",
      written_at: NOW,
    },
    measures: {
      references: 5,
      confidence: 0.9,
      firm: true,
      lines: [
        { id: "references", label: "Learnt from", value: "5 videos" },
        { id: "shots", label: "Shots", value: "Long takes that let the set run: 0 to 5 cuts a video, the longest shot about 20 s" },
        { id: "first_cut", label: "Opening shot", value: "The first cut comes about 2.3 s in" },
        { id: "effects", label: "Effects", value: "None found: no zoom punches, flashes or speed changes" },
        { id: "title", label: "Title", value: "On 60 % of the videos, all capitals, colour #FFF4D6, at the top, one line, no other text" },
        { id: "ending", label: "Ending", value: "A long hold on the last shot, about 4.0 s" },
        { id: "transitions", label: "Transitions", value: "Plain cuts only" },
        { id: "camera", label: "Camera", value: "Handheld, it follows you" },
        { id: "cuts", label: "Cuts and reps", value: "Cuts mid-rep; switches angles within an exercise" },
        { id: "title_form", label: "Title form", value: "<topic> 7" },
        { id: "chapters", label: "Chapters", value: "Numbered chapter titles in videos with several exercises" },
        { id: "music", label: "Music", value: "Music on every video, quiet for about 2.9 s, then it lifts, builds to the end" },
        { id: "length", label: "Length", value: "15 s to 44 s (usually about 26 s)" },
      ],
    },
    settings: { transitions: "lots" },
    effective: {
      ...base.effective,
      transitions: "lots",
      title_colour: "#FFF4D6",
      title_position: "top",
      title_case: "upper",
      title_template: "<TOPIC> 7",
      cut_timing: "mid_rep",
      sources: {
        ...base.effective.sources,
        vibe: "suggested",
        transitions: "you",
        title_colour: "suggested",
        title_position: "suggested",
        title_case: "suggested",
        music: "suggested",
        pace: "suggested",
        chapter_titles: "measured",
        title_template: "measured",
        ending_hold: "measured",
        cut_timing: "measured",
        angle_switches: "measured",
      },
    },
    references: [1, 2, 3, 4, 5].map((n) => styleReference(n)),
    learned_at: NOW,
    ...over,
  };
}
