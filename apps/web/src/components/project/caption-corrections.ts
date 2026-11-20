/**
 * The creator's fixes of misheard caption words (Captions v2). A fix is `{ asset_id, word, text }`: the word at that position of the
 * asset's transcript is shown as `text`. It is saved as a `set_captions` op that replaces the whole list. The server refuses a text that is
 * not clean (the schema, the op and the renderer all check it), so the helpers here give the creator the reason before the request.
 * The limits are the schema's (`captionCorrections` in packages/schemas/timeline/v1.schema.json).
 */

/** The longest replacement of a word, in characters, and the most fixes a video holds. */
export const MAX_CORRECTION_LENGTH = 40;
export const MAX_CORRECTIONS = 200;

/** One fix, as the timeline spec holds it. */
export interface CaptionCorrection {
  asset_id: string;
  word: number;
  text: string;
  /** The word as the transcript had it: the server skips the fix when the transcript no longer has this word there. */
  was?: string;
}

// The server's rule (timeline.CleanCorrection): no control, format (right-to-left overrides, zero width), line or paragraph separator,
// private-use or surrogate character, no replacement character, and none of the characters that matter in markup, templates and scripts.
// Punctuation at the ends is fine: "Dr.", "yes," and "..." are transcript words.
const MARKUP = "<>{}\\|^~[]$`";
const INVISIBLE = /[\p{Cc}\p{Cf}\p{Co}\p{Cs}\p{Zl}\p{Zp}]/u;
const REPLACEMENT = String.fromCodePoint(0xfffd);
const isBadCharacter = (ch: string): boolean => MARKUP.includes(ch) || ch === REPLACEMENT || INVISIBLE.test(ch);

export type CleanedCorrection = { ok: true; text: string } | { ok: false; reason: string };

/** Cleans what the creator typed: white space becomes single spaces, the ends are trimmed, and a text the server would refuse is explained. */
export function cleanCorrection(raw: string): CleanedCorrection {
  const text = raw.normalize("NFC").replace(/\s+/gu, " ").trim();
  if (text === "") return { ok: false, reason: "Type the word as it should read." };
  if ([...text].length > MAX_CORRECTION_LENGTH) return { ok: false, reason: `Keep it to ${MAX_CORRECTION_LENGTH} characters.` };
  if ([...text].some(isBadCharacter)) return { ok: false, reason: "Use letters, numbers and ordinary punctuation only." };
  return { ok: true, text };
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);

/** The enabled captions track of a spec and the fixes it holds, or undefined when the video has no captions. Read defensively: a spec is data. */
export function captionsOf(spec: unknown): { corrections: CaptionCorrection[] } | undefined {
  if (!isRecord(spec) || !Array.isArray(spec.tracks)) return undefined;
  const track = spec.tracks.find((t) => isRecord(t) && t.type === "captions" && t.enabled === true);
  if (!isRecord(track)) return undefined;
  const corrections: CaptionCorrection[] = [];
  if (Array.isArray(track.corrections)) {
    for (const c of track.corrections) {
      if (isRecord(c) && typeof c.asset_id === "string" && typeof c.text === "string" && typeof c.word === "number" && Number.isInteger(c.word)) {
        corrections.push({ asset_id: c.asset_id, word: c.word, text: c.text, ...(typeof c.was === "string" ? { was: c.was } : {}) });
      }
    }
  }
  return { corrections };
}

/** The assets the clips of the main track play, in order of first use: the ones whose words the captions can show. */
export function clipAssetIds(spec: unknown): string[] {
  if (!isRecord(spec) || !Array.isArray(spec.tracks)) return [];
  const main = spec.tracks.find((t) => isRecord(t) && t.type === "video");
  if (!isRecord(main) || !Array.isArray(main.items)) return [];
  const ids: string[] = [];
  for (const item of main.items) {
    if (isRecord(item) && typeof item.asset_id === "string" && !ids.includes(item.asset_id)) ids.push(item.asset_id);
  }
  return ids;
}

/** The fixes of one asset by word position. */
export function correctionsOf(list: readonly CaptionCorrection[], assetId: string): Map<number, string> {
  return new Map(list.filter((c) => c.asset_id === assetId).map((c) => [c.word, c.text]));
}

/**
 * The list with the word fixed to `text`. A text equal to the original word, or no text, takes the fix away. Returns the list unchanged
 * when it would hold more than {@link MAX_CORRECTIONS}.
 */
export function withCorrection(list: readonly CaptionCorrection[], assetId: string, word: number, text: string | null, original: string): CaptionCorrection[] {
  const rest = list.filter((c) => !(c.asset_id === assetId && c.word === word));
  if (text === null || text === original.trim()) return rest;
  if (rest.length >= MAX_CORRECTIONS) return [...list];
  const fix: CaptionCorrection = { asset_id: assetId, word, text, ...([...original].length <= MAX_WAS_LENGTH ? { was: original } : {}) };
  return [...rest, fix].sort((a, b) => a.asset_id.localeCompare(b.asset_id) || a.word - b.word);
}

/** The longest transcript word a fix remembers (the schema's `was`). */
export const MAX_WAS_LENGTH = 80;

/** Whether two lists hold the same fixes: a save of an unchanged list is not sent. */
export function sameCorrections(a: readonly CaptionCorrection[], b: readonly CaptionCorrection[]): boolean {
  return a.length === b.length && a.every((c, i) => c.asset_id === b[i]?.asset_id && c.word === b[i]?.word && c.text === b[i]?.text && c.was === b[i]?.was);
}

/** Whether a new fix would be the one past the most the video holds (a fix of a word that already has one replaces it). */
export function atLimit(list: readonly CaptionCorrection[], assetId: string, word: number): boolean {
  return list.length >= MAX_CORRECTIONS && !list.some((c) => c.asset_id === assetId && c.word === word);
}

/** The op that saves the list: it replaces every fix the captions track has. */
export function setCaptionsOp(corrections: readonly CaptionCorrection[]): { op: "set_captions"; corrections: CaptionCorrection[] } {
  return { op: "set_captions", corrections: [...corrections] };
}
