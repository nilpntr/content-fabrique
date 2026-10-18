/**
 * The words of "Your style" (P6-05b): the vibes, the settings and their options with a one-line explanation each, where a setting came
 * from, and the state of the reference videos. Plain words for a creator; tested in style-words.test.ts.
 */
import type { CreatorStyle, EditVibe, StyleReference, StyleSettingSource, StyleSettings } from "@/lib/api/client";

// ---- The vibe --------------------------------------------------------------------------------------------------------------------

export interface VibeWords {
  value: EditVibe;
  label: string;
  /** One line under the label. */
  hint: string;
}

export const VIBES: readonly VibeWords[] = [
  { value: "calm", label: "Calm", hint: "Long shots, gentle cuts, a quiet title." },
  { value: "lively", label: "Lively", hint: "Cuts on the beat, a little movement, titles that pop in." },
  { value: "hype", label: "Hype", hint: "Fast cuts, lots of movement, big energy." },
];

export const vibeLabel = (vibe: EditVibe): string => VIBES.find((v) => v.value === vibe)?.label ?? vibe;

// ---- The settings ----------------------------------------------------------------------------------------------------------------

/** The settings with a fixed list of choices (the title colour is a colour, not a list). */
export type ChoiceSetting = Exclude<keyof StyleSettings, "title_colour" | "vibe" | "title_template">;

export interface ChoiceWords {
  key: ChoiceSetting;
  label: string;
  /** One line that says what the setting does. */
  explain: string;
  options: readonly { value: string; label: string }[];
}

export const SETTINGS: readonly ChoiceWords[] = [
  {
    key: "transitions",
    label: "Transitions",
    explain: "Swipes, zooms and flashes between shots. Off keeps plain cuts on the beat, the way most creators edit.",
    options: [
      { value: "off", label: "Off" },
      { value: "subtle", label: "A few" },
      { value: "lots", label: "Lots" },
    ],
  },
  {
    key: "text_animation",
    label: "Moving titles",
    explain: "Titles pop or slide in instead of just appearing.",
    options: [
      { value: "off", label: "Off" },
      { value: "on", label: "On" },
    ],
  },
  {
    key: "camera_motion",
    label: "Camera moves",
    explain: "Slow zooms and punches on the beat, so the picture never sits still.",
    options: [
      { value: "off", label: "Off" },
      { value: "subtle", label: "A little" },
      { value: "lots", label: "Lots" },
    ],
  },
  {
    key: "title_position",
    label: "Title place",
    explain: "Where the title sits. It never goes over you.",
    options: [
      { value: "top", label: "Top" },
      { value: "upper_third", label: "Upper third" },
      { value: "auto", label: "Let the editor pick" },
    ],
  },
  {
    key: "title_case",
    label: "Title letters",
    explain: "How the title is written.",
    options: [
      { value: "upper", label: "ALL CAPS" },
      { value: "lower", label: "all lowercase" },
      { value: "as_typed", label: "As typed" },
    ],
  },
  {
    key: "chapter_titles",
    label: "Chapter titles",
    explain: "A numbered title for each exercise when a video has several.",
    options: [
      { value: "off", label: "Off" },
      { value: "on", label: "On" },
    ],
  },
  {
    key: "ending_hold",
    label: "Ending",
    explain: "How long the last shot stays on screen.",
    options: [
      { value: "short", label: "Quick" },
      { value: "long", label: "Long hold" },
    ],
  },
  {
    key: "cut_timing",
    label: "Cuts and reps",
    explain: "Where a cut falls while you train.",
    options: [
      { value: "full_reps", label: "After a full rep" },
      { value: "mid_rep", label: "On the beat, mid-rep" },
    ],
  },
  {
    key: "angle_switches",
    label: "Angles",
    explain: "Switch between camera angles within one exercise.",
    options: [
      { value: "off", label: "One angle" },
      { value: "on", label: "Switch" },
    ],
  },
  {
    key: "music",
    label: "Music",
    explain: "Whether your video is cut to music.",
    options: [
      { value: "always", label: "Always" },
      { value: "when_added", label: "When I add a song" },
      { value: "never", label: "Never" },
    ],
  },
  {
    key: "pace",
    label: "Pace",
    explain: "How quickly the shots change, compared with your own videos.",
    options: [
      { value: "slower", label: "Slower" },
      { value: "like_mine", label: "Like my videos" },
      { value: "faster", label: "Faster" },
    ],
  },
];

/** A few title colours to pick from, and the creator's own. */
export const TITLE_COLOURS: readonly { value: string; label: string }[] = [
  { value: "#FFFFFF", label: "White" },
  { value: "#FFF4D6", label: "Cream" },
  { value: "#FFD43B", label: "Yellow" },
  { value: "#FF6B6B", label: "Coral" },
  { value: "#4DABF7", label: "Blue" },
  { value: "#111111", label: "Black" },
];

const HEX = /^#[0-9A-Fa-f]{6}$/;
export const isHexColour = (value: string): boolean => HEX.test(value);

/** A title form for people: the slots in small words ("<topic> 7"). */
export const templateWords = (template: string): string =>
  template.replace(/<N>/g, "<number>").replace(/<([A-Z]{1,12})>/g, (_, slot: string) => `<${slot.toLowerCase()}>`);

/** A title form as the API takes it: the slots in capitals ("<TOPIC> 7"), the spaces evened out. */
export const templateFromWords = (text: string): string =>
  text
    .trim()
    .replace(/\s+/g, " ")
    .replace(/<number>/gi, "<N>")
    .replace(/<([a-z]{1,12})>/gi, (_, slot: string) => `<${slot.toUpperCase()}>`);

/** Whether a typed title form can be saved: some text, at most 80 characters, slots in angle brackets. */
export function validTemplate(text: string): boolean {
  const t = templateFromWords(text);
  if (t === "" || t.length > 80 || !/^[\p{L}\p{N} <>.,:;!?'&()/+\-#%]+$/u.test(t)) return false;
  const opens = (t.match(/</g) ?? []).length;
  return opens === (t.match(/>/g) ?? []).length && opens === (t.match(/<[A-Z]{1,12}>/g) ?? []).length;
}

/** The name of a colour when it is one of ours, else the code. */
export const colourName = (hex: string): string => TITLE_COLOURS.find((c) => c.value.toLowerCase() === hex.toLowerCase())?.label ?? hex.toUpperCase();

/**
 * Where an effective setting came from, in a few words; null when the creator chose it (the control shows it as theirs, and needs no
 * note).
 */
export function sourceNote(source: StyleSettingSource): string | null {
  switch (source) {
    case "you":
      return null;
    case "suggested":
      return "Suggested from your videos";
    case "measured":
      return "From your videos";
    case "vibe":
      return "From your vibe";
    case "this_video":
      return "For this video";
    default:
      return "Default";
  }
}

/** The settings with one changed: `null` takes the creator's choice away (the suggestion decides again). */
export function withSetting(settings: StyleSettings, key: keyof StyleSettings, value: string | null): StyleSettings {
  const next: Record<string, string> = { ...(settings as Record<string, string>) };
  if (value === null) delete next[key];
  else next[key] = value;
  return next as StyleSettings;
}

/**
 * The style as it will be once new settings are saved, for the controls to move at once: every setting the creator chose shows as
 * theirs. A setting given back to the suggestion keeps its value until the server says what the suggestion is.
 */
export function withMySettings(style: CreatorStyle, settings: StyleSettings): CreatorStyle {
  const effective = { ...style.effective, sources: { ...style.effective.sources } } as CreatorStyle["effective"];
  const eff = effective as unknown as Record<string, unknown>;
  const sources = effective.sources as unknown as Record<string, StyleSettingSource>;
  for (const [key, value] of Object.entries(settings)) {
    if (value === undefined || value === null) continue;
    eff[key] = value;
    sources[key] = "you";
  }
  return { ...style, settings, effective };
}

/** How many settings the creator chose themselves. */
export const chosenCount = (settings: StyleSettings): number => Object.values(settings).filter((v) => v !== undefined && v !== null).length;

// ---- The reference videos --------------------------------------------------------------------------------------------------------

export function referenceStatus(ref: Pick<StyleReference, "status" | "progress" | "failure_reason">): string {
  switch (ref.status) {
    case "uploading":
      return "Uploading…";
    case "processing":
      return ref.progress > 1 ? `Getting it ready… ${ref.progress} %` : "Getting it ready…";
    case "analysing":
      return "Learning from it…";
    case "ready":
      return "Learnt";
    default:
      return failureWords(ref.failure_reason);
  }
}

/** Why a reference could not be used, in words. */
export function failureWords(reason: string | null | undefined): string {
  switch (reason) {
    case "too_long":
      return "Too long: use a video of 3 minutes or less";
    case "no_video_stream":
    case "undecodable_video":
    case "unsupported_format":
    case "corrupt_media":
      return "This file couldn't be read as a video";
    case "upload_expired":
    case "upload_aborted":
    case "size_mismatch":
      return "The upload didn't finish";
    default:
      return "Couldn't learn from this one";
  }
}

/** The line under the reference videos: how many there are and how many more fit. */
export function referencesLine(style: Pick<CreatorStyle, "references" | "limits">): string {
  const counted = style.references.filter((r) => r.status !== "failed").length;
  const left = Math.max(style.limits.max_references - counted, 0);
  if (counted === 0) return `Add 3 to ${style.limits.max_references} of your own finished videos.`;
  if (left === 0) return `${counted} videos. That's the most: remove one to add another.`;
  if (counted < 3) return `${counted} ${counted === 1 ? "video" : "videos"}. Add ${3 - counted} more for the best result.`;
  return `${counted} videos. You can add ${left} more.`;
}

/** The progress while the style is being learnt. */
export function learningLine(style: Pick<CreatorStyle, "status" | "progress">): string | null {
  if (style.status !== "learning") return null;
  const { done, total } = style.progress;
  if (total === 0 || done >= total) return "Writing your style…";
  return `Learning your style… ${done} of ${total} ${total === 1 ? "video" : "videos"} done`;
}

/** The limits of a reference video, for the file picker's help line. */
export function referenceLimitsLine(style: Pick<CreatorStyle, "limits">): string {
  const gb = style.limits.max_size_bytes / 2 ** 30;
  const minutes = Math.round(style.limits.max_duration_ms / 60_000);
  return `Videos up to ${minutes} minutes and ${gb >= 1 ? `${gb} GB` : `${Math.round(gb * 1024)} MB`} each.`;
}
