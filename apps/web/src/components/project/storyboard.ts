/**
 * The plan as a storyboard: a few short lines a creator reads at a glance ("Opens on your back pose with the title BACK DAY 101",
 * "Your seated row, cut on the music", "Ends on your lat pulldown"). It is made from the editor's creative plan when there is one,
 * else from the clips of the latest version. All the wording is here, so it can be tested and translated in one place.
 */
import type { VideoItem } from "@/components/editor/types";
import type { CreativePlan } from "./run-artifacts";

/** A text on screen (a `text` item of the timeline): the title, a caption. */
export interface ScreenText {
  at_ms: number;
  text: string;
}

export interface StoryCard {
  key: string;
  line: string;
  /** The clip it shows, for its frame. */
  assetId?: string;
  place: "first" | "middle" | "last";
}

/** The idea of the video, in plain words: "Transformation reveal", and why it fits. */
export interface StoryIdea {
  name: string;
  why?: string;
  /** "with a seamless loop". */
  mix?: string;
  /** The creator's own videos never use this format. */
  isNew: boolean;
}

export interface Storyboard {
  /** The format the editor chose, when the plan names one. */
  idea?: StoryIdea;
  /** The editor's story in a sentence, when the plan has one. */
  intro?: string;
  /** "About 25 seconds". */
  length?: string;
  cards: StoryCard[];
  /** Moments left out of a long board ("and 4 more moments"). */
  more: number;
}

/** A board longer than this shows its first {@link HEAD} cards, the count of the rest, and the ending. */
const MAX_CARDS = 8;
const HEAD = 6;

const DETERMINERS = /^(the|a|an|your|my|his|her|their|our|this|that)\s/i;

/** "Heaviest lat pulldown" as "heaviest lat pulldown"; an acronym ("BACK DAY") keeps its capitals. */
function lowerFirst(text: string): string {
  return /^[A-Z][A-Z]/.test(text) ? text : text.replace(/^./, (c) => c.toLowerCase());
}

const upperFirst = (text: string): string => text.replace(/^./, (c) => c.toUpperCase());

/** What a shot shows, as the creator's own: "your heaviest lat pulldown". A description that already has an article keeps it. */
export function subjectOf(what: string): string {
  const clean = what.trim().replace(/\s+/g, " ").replace(/[.!]$/, "");
  return DETERMINERS.test(clean) ? lowerFirst(clean) : `your ${lowerFirst(clean)}`;
}

/** How an effect reads, after a comma. Effects this list does not know are left out: the line stays true without them. */
const EFFECTS: Readonly<Record<string, string>> = {
  punch_in: "with a quick zoom",
  push_in: "slowly zooming in",
  pull_back: "zooming out",
  shake: "with a shake on the hit",
  speed_ramp: "fast, then in slow motion",
  whip: "with a quick swipe in",
  whip_left: "with a quick swipe in",
  whip_right: "with a quick swipe in",
  zoom_through: "zooming through",
  flash: "with a flash",
  crossfade: "fading in",
  dip_to_black: "after a fade to black",
};

export interface ShotWords {
  place: "first" | "middle" | "last";
  /** The plan's role: hook, build, peak, detail, ending... */
  role?: string;
  /** What the shot shows, a few words. */
  what: string;
  effect?: string;
  /** A text that starts during the shot. */
  text?: string;
  /** The video is cut to a beat. */
  musical: boolean;
}

/** One line of the storyboard. */
export function describeShot(shot: ShotWords): string {
  const subject = subjectOf(shot.what);
  const extras: string[] = [];
  const effect = shot.effect ? EFFECTS[shot.effect] : undefined;
  let line: string;
  if (shot.place === "first") {
    line = `Opens on ${subject}`;
    if (shot.text) line += ` with the title ${oneLine(shot.text)}`;
  } else if (shot.place === "last") {
    line = `Ends on ${subject}`;
  } else if (shot.role === "peak") {
    line = `Peaks on ${subject}`;
  } else if (shot.role === "detail") {
    line = `A closer look at ${subject}`;
  } else {
    line = upperFirst(subject);
    if (shot.musical) extras.push("cut on the music");
  }
  if (effect) extras.push(effect);
  if (shot.text && shot.place !== "first") extras.push(`with “${oneLine(shot.text)}” on screen`);
  return [line, ...extras].join(", ");
}

const oneLine = (text: string): string => text.replace(/\s*\n\s*/g, " ").trim();

const placeOf = (index: number, count: number): StoryCard["place"] => (index === 0 ? "first" : index === count - 1 && count > 1 ? "last" : "middle");

/** "About 25 seconds"; nothing under a second. */
export function lengthWords(ms: number | undefined): string | undefined {
  if (!ms || ms < 1000) return undefined;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `About ${seconds} seconds`;
  const minutes = Math.round(seconds / 60);
  return `About ${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
}

/** The text that starts inside `[start, end)`, the first one. */
function textIn(texts: readonly ScreenText[], start: number, end: number): string | undefined {
  return texts.find((text) => text.at_ms >= start && text.at_ms < end)?.text;
}

function fold(cards: StoryCard[]): { cards: StoryCard[]; more: number } {
  if (cards.length <= MAX_CARDS) return { cards, more: 0 };
  const last = cards.at(-1) as StoryCard;
  return { cards: [...cards.slice(0, HEAD), last], more: cards.length - HEAD - 1 };
}

/** The board from the editor's plan. Undefined when a shot has nothing to call it by (a plan known only from a cut-off summary). */
export function storyboardFromPlan(plan: CreativePlan, assetTitles: ReadonlyMap<string, string>, texts: readonly ScreenText[] = []): Storyboard | undefined {
  if (plan.shots.length === 0) return undefined;
  const named = plan.shots.map((shot) => shot.what ?? (shot.assetId ? assetTitles.get(shot.assetId) : undefined));
  if (named.some((what) => !what)) return undefined;
  const timed = plan.shots.every((shot) => shot.lengthMs !== undefined);
  let start = 0;
  const cards = plan.shots.map((shot, index): StoryCard => {
    const end = start + (shot.lengthMs ?? 0);
    const text = timed ? textIn(texts, index === 0 ? Number.NEGATIVE_INFINITY : start, end) : index === 0 ? texts[0]?.text : undefined;
    start = end;
    const place = placeOf(index, plan.shots.length);
    return {
      key: `${index}`,
      place,
      ...(shot.assetId ? { assetId: shot.assetId } : {}),
      line: describeShot({ place, role: shot.role, what: named[index] as string, effect: shot.effect, text, musical: Boolean(plan.tempoBpm) }),
    };
  });
  const length = lengthWords(plan.estimatedLengthMs ?? (plan.targetLengthS ? plan.targetLengthS * 1000 : undefined));
  const idea = storyIdea(plan);
  return { ...(idea ? { idea } : {}), ...(plan.story ? { intro: plan.story } : {}), ...(length ? { length } : {}), ...fold(cards) };
}

/** The plan's format as the storyboard says it. */
export function storyIdea(plan: CreativePlan | undefined): StoryIdea | undefined {
  const idea = plan?.idea;
  if (!idea) return undefined;
  return {
    name: idea.name,
    ...(idea.why ? { why: idea.why } : {}),
    ...(idea.mixName ? { mix: `with a ${lowerFirst(idea.mixName)}` } : {}),
    isNew: idea.newToCreator,
  };
}

/** "Version 3 · new idea: transformation reveal" for a version a new idea made; the version's name otherwise. */
export function versionLabel(versionName: string, plan: CreativePlan | undefined, newIdea: boolean): string {
  const idea = plan?.idea;
  if (!newIdea || !idea) return versionName;
  return `${versionName} · new idea: ${lowerFirst(idea.name)}`;
}

/** The effect of a clip on the timeline worth a word: its camera move, else a speed ramp, else how it comes in. */
function itemEffect(item: VideoItem): string | undefined {
  if (item.motion) return item.motion.preset;
  if (item.speed_ramp) return "speed_ramp";
  const transition = item.transition_in?.type;
  return transition && transition !== "cut" ? transition : undefined;
}

/** The board from the clips of a version, for a cut with no plan (an older edit, or one you made). */
export function storyboardFromItems(items: readonly VideoItem[], assetTitles: ReadonlyMap<string, string>, texts: readonly ScreenText[] = [], { musical = false, durationMs }: { musical?: boolean; durationMs?: number } = {}): Storyboard | undefined {
  if (items.length === 0) return undefined;
  const cards = items.map((item, index): StoryCard => {
    const next = items[index + 1];
    const place = placeOf(index, items.length);
    return {
      key: item.id,
      place,
      assetId: item.asset_id,
      line: describeShot({
        place,
        what: assetTitles.get(item.asset_id) ?? "a clip",
        effect: itemEffect(item),
        text: textIn(texts, index === 0 ? Number.NEGATIVE_INFINITY : item.at_ms, next ? next.at_ms : Number.POSITIVE_INFINITY),
        musical,
      }),
    };
  });
  const length = lengthWords(durationMs);
  return { ...(length ? { length } : {}), ...fold(cards) };
}

/** The plan when it can be told, else the clips of the latest version. */
export function storyboard({
  plan,
  items = [],
  texts = [],
  assetTitles,
  musical = false,
  durationMs,
}: {
  plan?: CreativePlan;
  items?: readonly VideoItem[];
  texts?: readonly ScreenText[];
  assetTitles: ReadonlyMap<string, string>;
  musical?: boolean;
  durationMs?: number;
}): Storyboard | undefined {
  return (plan ? storyboardFromPlan(plan, assetTitles, texts) : undefined) ?? storyboardFromItems(items, assetTitles, texts, { musical, durationMs });
}
