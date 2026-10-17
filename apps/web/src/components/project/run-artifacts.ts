import type { RunEvent } from "@/lib/api/client";

/**
 * What the editor made along the way, for the run feed: the creative plan (`plan_edit`), the self-review rounds (`check_edit`)
 * and the brightness match (`match_exposure`).
 *
 * The editor records the plan and every check as an `artifact` event with the full payload (docs/design/edit-style-v1.md
 * sections 6 and 7), but `GET /v1/runs/{id}` returns only a one-line summary of an event (`RunEvent.text`, 400 bytes, no
 * payload). Until the API carries the payload, this file reads what the summaries do carry: the head of the `plan_edit` call
 * (content type, story, hook), the head of its answer (playbook, tempo, target length and the first shots) and the head of
 * each `check_edit` answer (the counts, and the first checks). The cards show whatever is known and leave out the rest.
 *
 * {@link planFromArtifact} and {@link checkRoundFromArtifact} read the payloads as the design note documents them; they are
 * what the cards are tested with, and what a payload-carrying API will use.
 */

export interface PlanShot {
  role: string;
  /** What the shot is, a few words. */
  what?: string;
  /** The library clip the plan names. */
  assetId?: string;
  beats: number;
  lengthMs?: number;
  /** `none`, `punch_in`, `push_in`, `pull_back`, `shake`, `speed_ramp`, `whip`, `zoom_through` or `flash`. */
  effect?: string;
}

export interface CreativePlan {
  contentType: string;
  playbook?: string;
  story?: string;
  hook?: string;
  arc?: string;
  ending?: string;
  targetLengthS?: number;
  /** Null for a video without a beat grid (speech-led); undefined when not known. */
  tempoBpm?: number | null;
  pace?: string;
  estimatedLengthMs?: number;
  /** How many shots the plan has, which can be more than the shots known here. */
  shotCount?: number;
  shots: PlanShot[];
}

export type CheckStatus = "pass" | "borderline" | "fail" | "skip";

export interface CheckItem {
  id: string;
  status: CheckStatus;
  /** The reason in one line, written by the check. */
  detail?: string;
}

/** One run of `check_edit`: the editor's self-review of a version. */
export interface CheckRound {
  /** 1 for the first check of the run. */
  nth: number;
  version?: number;
  contentType?: string;
  targetLengthS?: number;
  /** "12 of 13 pass": `passed` of `applicable`. A check that does not apply is `skipped` and is not counted. */
  passed: number;
  failed: number;
  skipped: number;
  checks: CheckItem[];
  /** The edit-tool calls and brightness matches between the previous check and this one, in the order they ran. */
  fixes: Fix[];
}

export type Fix = "match_exposure" | "edit";

export const applicable = (round: Pick<CheckRound, "passed" | "failed">): number => round.passed + round.failed;

export interface BrightnessMatch {
  /** The clips whose brightness was nudged; 0 when there was nothing to change. */
  clips: number;
}

export interface RunArtifacts {
  plan?: CreativePlan;
  rounds: CheckRound[];
}

// ---- The documented payloads ----------------------------------------------------------------------------------------

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const str = (value: unknown): string | undefined => (typeof value === "string" && value.trim() !== "" ? value : undefined);
const int = (value: unknown): number | undefined => (typeof value === "number" && Number.isFinite(value) ? value : undefined);

/** The `data` of a `creative_plan` artifact (edit-style-v1 section 6), or undefined when it is not one this reader knows. */
export function planFromArtifact(data: unknown): CreativePlan | undefined {
  if (!isRecord(data) || data.version !== 1) return undefined;
  const contentType = str(data.content_type);
  if (!contentType) return undefined;
  const shots: PlanShot[] = [];
  for (const raw of Array.isArray(data.shots) ? data.shots : []) {
    if (!isRecord(raw)) continue;
    const beats = int(raw.beats);
    const role = str(raw.role);
    if (beats === undefined || !role) continue;
    const effect = str(raw.effect);
    shots.push({ role, beats, what: str(raw.what), assetId: str(raw.asset_id), lengthMs: int(raw.length_ms), ...(effect && effect !== "none" ? { effect } : {}) });
  }
  const tempo = int(data.tempo_bpm);
  return {
    contentType,
    playbook: str(data.playbook),
    story: str(data.story),
    hook: str(data.hook),
    arc: str(data.arc),
    ending: str(data.ending),
    targetLengthS: int(data.target_length_s),
    tempoBpm: tempo === undefined ? undefined : tempo > 0 ? tempo : null,
    pace: str(data.pace),
    estimatedLengthMs: int(data.estimated_length_ms),
    shotCount: shots.length,
    shots,
  };
}

const STATUSES: ReadonlySet<string> = new Set(["pass", "fail", "skip"]);

/** The `data` of an `edit_check` artifact (edit-style-v1 section 7). A pass whose reason starts with "borderline" is shown as borderline. */
export function checkRoundFromArtifact(data: unknown, nth: number): CheckRound | undefined {
  if (!isRecord(data) || data.record_version !== 1 || !Array.isArray(data.checks)) return undefined;
  const checks: CheckItem[] = [];
  for (const raw of data.checks) {
    if (!isRecord(raw)) continue;
    const id = str(raw.id);
    if (!id || typeof raw.status !== "string" || !STATUSES.has(raw.status)) continue;
    const detail = str(raw.detail);
    checks.push({ id, status: raw.status === "pass" && detail && /^borderline\b/i.test(detail) ? "borderline" : (raw.status as CheckStatus), ...(detail ? { detail } : {}) });
  }
  const count = (status: CheckStatus[]) => checks.filter((check) => status.includes(check.status)).length;
  return {
    nth,
    version: int(data.version),
    contentType: str(data.content_type),
    targetLengthS: int(data.target_length_s),
    passed: int(data.passed) ?? count(["pass", "borderline"]),
    failed: int(data.failed) ?? count(["fail"]),
    skipped: int(data.skipped) ?? count(["skip"]),
    checks,
    fixes: [],
  };
}

// ---- What the one-line summaries carry -------------------------------------------------------------------------------

/** True when a summary was cut at the 400-byte limit (it ends with an ellipsis). */
const wasCut = (text: string): boolean => text.endsWith("…");

/** The complete string value of `"key":"..."` in a possibly cut JSON text. A string that was cut mid-way is not returned. */
function stringField(text: string, key: string): string | undefined {
  const match = new RegExp(`"${key}"\\s*:\\s*("(?:[^"\\\\]|\\\\.)*")`).exec(text);
  if (!match?.[1]) return undefined;
  try {
    return str(JSON.parse(match[1]));
  } catch {
    return undefined;
  }
}

function numberField(text: string, key: string): number | undefined {
  const match = new RegExp(`"${key}"\\s*:\\s*(-?\\d+)(?=[,}\\s]|$)`).exec(text);
  return match?.[1] ? Number(match[1]) : undefined;
}

/** The plan from the head of the `plan_edit` call and of its answer. The call is `plan_edit {"content_type":...`; the answer starts `Plan saved (...). Content type X, playbook Y.` */
export function planFromSummaries(call: string | undefined, result: string | undefined): CreativePlan | undefined {
  const answer = result ?? "";
  const contentType = stringField(call ?? "", "content_type") ?? /Content type ([a-z_]+)/.exec(answer)?.[1];
  if (!contentType) return undefined;

  const tempo = /Tempo: (\d+) BPM/.exec(answer)?.[1];
  const list = /Shot list, (\d+) shots?, ([\d.]+) s in all \(target (\d+) s\)/.exec(answer);
  const shots: PlanShot[] = [];
  const head = list ? answer.slice(list.index + list[0].length) : "";
  // "1. hook: 3 beats = 1.45 s, punch_in 2. build: ..." (the lines were joined into one). A shot that was cut at the end (the
  // summary then ends with an ellipsis) does not match the end of the text, and is left out.
  const shot = /(\d+)\. ([a-z]+): (\d+) beats = ([\d.]+) s(?:, ([a-z_]+))?(?=\s+\d+\. |\s*$)/g;
  for (const match of head.matchAll(shot)) {
    const [, , role, beats, seconds, effect] = match;
    if (!role || !beats || !seconds) continue;
    shots.push({ role, beats: Number(beats), lengthMs: Math.round(Number(seconds) * 1000), ...(effect ? { effect } : {}) });
  }

  return {
    contentType,
    playbook: /, playbook ([a-z_]+)\.v\d+/.exec(answer)?.[1] ?? stringField(call ?? "", "playbook"),
    story: stringField(call ?? "", "story"),
    hook: stringField(call ?? "", "hook"),
    arc: stringField(call ?? "", "arc"),
    ending: stringField(call ?? "", "ending"),
    targetLengthS: list?.[3] ? Number(list[3]) : numberField(call ?? "", "target_length_s"),
    tempoBpm: tempo ? Number(tempo) : /No beat grid/.test(answer) ? null : numberField(call ?? "", "tempo_bpm"),
    pace: stringField(call ?? "", "pace"),
    estimatedLengthMs: list?.[2] ? Math.round(Number(list[2]) * 1000) : undefined,
    shotCount: list?.[1] ? Number(list[1]) : undefined,
    shots,
  };
}

const CHECK_HEAD = /check_edit for version (\d+) \(([a-z_]+)(?:, target (\d+) s)?\): (\d+) of (\d+) checks pass, (\d+) fail, (\d+) do not apply\./;

/** The round from the head of a `check_edit` answer: the counts, then `PASS id: reason FAIL id: reason n/a id: reason`. The check cut at the end is left out. */
export function checkRoundFromSummary(text: string, nth: number): CheckRound | undefined {
  const head = CHECK_HEAD.exec(text);
  if (!head) return undefined;
  const [, version, contentType, target, passed, , failed, skipped] = head;
  const checks: CheckItem[] = [];
  const rest = text.slice(head.index + head[0].length);
  const marks = [...rest.matchAll(/(?:^|\s)(PASS|FAIL|n\/a) ([a-z_]+): /g)];
  marks.forEach((mark, i) => {
    const next = marks[i + 1];
    const start = mark.index + mark[0].length;
    const isLast = next === undefined;
    if (isLast && wasCut(text)) return;
    const detail = rest.slice(start, next ? next.index : undefined).trim();
    const word = mark[1];
    const id = mark[2];
    if (!id) return;
    const status: CheckStatus = word === "FAIL" ? "fail" : word === "PASS" ? (/^borderline\b/i.test(detail) ? "borderline" : "pass") : "skip";
    checks.push({ id, status, ...(detail ? { detail } : {}) });
  });
  return { nth, version: Number(version), contentType, ...(target ? { targetLengthS: Number(target) } : {}), passed: Number(passed), failed: Number(failed), skipped: Number(skipped), checks, fixes: [] };
}

/** "Matched the brightness of 3 clips toward ..." / "Nothing to change: ..." / "Nothing to match: ...". */
export function matchFromSummary(text: string): BrightnessMatch | undefined {
  const matched = /^Matched the brightness of (\d+) clips?/.exec(text);
  if (matched?.[1]) return { clips: Number(matched[1]) };
  return /^Nothing to (?:change|match)/.test(text) ? { clips: 0 } : undefined;
}

// ---- The run ---------------------------------------------------------------------------------------------------------

/**
 * The answer of the call at `index`: the next `tool_result` of the same tool, before another call of it. A failed answer is not one.
 * The events carry no call id, so this is by order, which is safe because the runtime writes a call and its result one after the
 * other (the call, the run of the tool, the result) before it starts the next call.
 */
export function answerTo(events: readonly RunEvent[], index: number): RunEvent | undefined {
  const call = events[index];
  for (let i = index + 1; i < events.length; i++) {
    const event = events[i];
    if (!event) continue;
    if (event.type === "tool_call" && event.tool === call?.tool) return undefined;
    if (event.type === "tool_result" && event.tool === call?.tool) return event.is_error ? undefined : event;
  }
  return undefined;
}

/** The plan and the check rounds (with what ran between them, a brightness match included) of a run, from its events (oldest first). */
export function runArtifacts(events: readonly RunEvent[]): RunArtifacts {
  let plan: CreativePlan | undefined;
  const rounds: CheckRound[] = [];
  let pending: Fix[] = [];
  events.forEach((event, index) => {
    if (event.type !== "tool_call") return;
    if (event.tool === "plan_edit") {
      const answer = answerTo(events, index);
      // A plan the tool refused is no plan. The last accepted one is the plan.
      const read = answer ? planFromSummaries(event.text, answer.text) : undefined;
      if (read) plan = read;
    } else if (event.tool === "check_edit") {
      const answer = answerTo(events, index);
      const round = answer ? checkRoundFromSummary(answer.text, rounds.length + 1) : undefined;
      if (round) {
        rounds.push({ ...round, fixes: pending });
        pending = [];
      }
    } else if (event.tool === "match_exposure") {
      const answer = answerTo(events, index);
      const read = answer ? matchFromSummary(answer.text) : undefined;
      if (read && rounds.length > 0 && read.clips > 0) pending = [...pending, "match_exposure"];
    } else if (event.tool === "edit_timeline" && rounds.length > 0) {
      // Edits before the first check are the editor's first pass, not a fix.
      if (answerTo(events, index)) pending = pending.at(-1) === "edit" ? pending : [...pending, "edit"];
    }
  });
  return { ...(plan ? { plan } : {}), rounds };
}
