import { ClipboardList, Gauge, Music2, Timer } from "lucide-react";
import { EffectIcon, MOTION, TRANSITIONS, effectName, RampIcon } from "@/components/editor/effects";
import { cn } from "@/lib/utils";
import { Pill } from "./badges";
import type { CreativePlan, PlanShot } from "./run-artifacts";

const CONTENT_TYPES: Readonly<Record<string, string>> = {
  talking: "Talking head",
  unboxing_product: "Unboxing",
  workout: "Workout",
  vlog: "Vlog",
  tutorial: "Tutorial",
  general: "General",
};

/** "unboxing_product" as "Unboxing"; a type this build does not know reads as its name. */
export const contentTypeName = (type: string): string => CONTENT_TYPES[type] ?? type.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());

/** How a shot's effect reads: the plan names `speed_ramp`, `whip`, `zoom_through` and `flash` next to the motion presets. */
function ShotEffect({ effect }: { effect: string }) {
  if (effect === "speed_ramp")
    return (
      <span className="inline-flex items-center gap-1 text-muted-foreground">
        <RampIcon /> Speed ramp
      </span>
    );
  const table = MOTION[effect] ? MOTION : TRANSITIONS;
  return (
    <span className="inline-flex items-center gap-1 text-muted-foreground">
      <EffectIcon table={table} name={effect} /> {effectName(table, effect)}
    </span>
  );
}

/** A beat is a pip: a shot of three beats is three pips, so the rhythm of the plan can be read down the list. */
function Beats({ beats }: { beats: number }) {
  const shown = Math.min(beats, 12);
  return (
    <span className="inline-flex items-center gap-2 tabular-nums">
      <span aria-hidden className="flex gap-0.5">
        {Array.from({ length: shown }, (_, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static pips, the list never reorders.
          <span key={i} className="h-2.5 w-1 rounded-[1px] bg-brand/70" />
        ))}
      </span>
      <span className="text-muted-foreground">
        {beats} {beats === 1 ? "beat" : "beats"}
      </span>
    </span>
  );
}

function ShotRow({ shot, index, title }: { shot: PlanShot; index: number; title?: string }) {
  const what = title ?? shot.what;
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5 sm:grid sm:grid-cols-[1.25rem_4.75rem_minmax(0,1fr)_7.5rem_6.5rem] sm:gap-x-2.5">
      <span className="w-5 font-mono text-[12px] text-subtle-foreground tabular-nums">{index + 1}</span>
      <span>
        <Pill tone={shot.role === "hook" ? "brand" : "neutral"} className="capitalize">
          {shot.role}
        </Pill>
      </span>
      <span className="min-w-0 flex-1 truncate text-[13px] sm:flex-none" title={what}>
        {what}
      </span>
      <span className="min-h-4 text-[12px]">{shot.effect ? <ShotEffect effect={shot.effect} /> : null}</span>
      <span className="text-[12px] sm:justify-self-end">
        <Beats beats={shot.beats} />
      </span>
    </li>
  );
}

function Story({ label, children }: { label: string; children: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-[10.5px] font-medium tracking-[0.08em] text-subtle-foreground uppercase">{label}</dt>
      <dd className="mt-0.5 text-[13px] leading-5 text-pretty">{children}</dd>
    </div>
  );
}

const seconds = (ms: number): string => `${(ms / 1000).toFixed(1)} s`;

/**
 * The editor's creative plan, as it showed the creator before cutting: the kind of video, the story in a line, the hook, the arc
 * and the ending, the tempo and the length it aims for, and the shot list (the clip, the effect and the length in beats).
 * Whatever the plan does not carry is left out, so a plan known in part reads as a shorter card.
 */
export function CreativePlanCard({ plan, assetTitles, className }: { plan: CreativePlan; assetTitles?: ReadonlyMap<string, string>; className?: string }) {
  const playbook = plan.playbook ?? plan.contentType;
  const hasStory = plan.hook || plan.arc || plan.ending;
  const unknown = plan.shotCount !== undefined ? plan.shotCount - plan.shots.length : 0;
  return (
    <section aria-label="Creative plan" data-testid="creative-plan" className={cn("space-y-3 rounded-xl bg-well p-3.5 shadow-well", className)}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <ClipboardList aria-hidden className="size-4 text-brand" />
        <h3 className="text-[13.5px] font-semibold tracking-[-0.01em]">The plan</h3>
        <Pill tone="brand" data-slot="content-type">
          {contentTypeName(plan.contentType)}
        </Pill>
        <Pill data-slot="playbook">{contentTypeName(playbook)} playbook</Pill>
      </div>

      {plan.story ? <p className="max-w-prose text-[14.5px] leading-snug font-medium tracking-[-0.01em] text-pretty">{plan.story}</p> : null}

      {hasStory ? (
        <dl className="grid gap-x-6 gap-y-2.5 md:grid-cols-3">
          {plan.hook ? <Story label="Hook">{plan.hook}</Story> : null}
          {plan.arc ? <Story label="Arc">{plan.arc}</Story> : null}
          {plan.ending ? <Story label="Ending">{plan.ending}</Story> : null}
        </dl>
      ) : null}

      <ul aria-label="Tempo and length" className="flex flex-wrap gap-1.5">
        {plan.tempoBpm !== undefined ? (
          <li>
            <Pill tone="strong" className="tabular-nums">
              <Music2 aria-hidden className="size-3" />
              {plan.tempoBpm ? `${plan.tempoBpm} BPM` : "No beat grid"}
            </Pill>
          </li>
        ) : null}
        {plan.targetLengthS ? (
          <li>
            <Pill tone="strong" className="tabular-nums">
              <Timer aria-hidden className="size-3" />
              {plan.targetLengthS} s target
              {plan.estimatedLengthMs ? <span className="font-normal text-muted-foreground"> · {seconds(plan.estimatedLengthMs)} planned</span> : null}
            </Pill>
          </li>
        ) : null}
        {plan.pace ? (
          <li>
            <Pill tone="strong">
              <Gauge aria-hidden className="size-3" />
              {plan.pace} pace
            </Pill>
          </li>
        ) : null}
      </ul>

      {plan.shots.length > 0 ? (
        <div>
          <h4 className="mb-0.5 text-[10.5px] font-medium tracking-[0.08em] text-subtle-foreground uppercase">
            Shot list{plan.shotCount ? <span className="tracking-normal normal-case"> · {plan.shotCount} shots</span> : null}
          </h4>
          <ol className="divide-y divide-hairline">
            {plan.shots.map((shot, index) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: a shot has no id, and the list is read-only.
              <ShotRow key={`${shot.role}-${index}`} shot={shot} index={index} title={shot.assetId ? assetTitles?.get(shot.assetId) : undefined} />
            ))}
          </ol>
          {unknown > 0 ? <p className="pt-1 text-[12px] text-subtle-foreground">+ {unknown} more {unknown === 1 ? "shot" : "shots"}</p> : null}
        </div>
      ) : null}
    </section>
  );
}
