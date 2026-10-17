import {
  CircleAlert,
  Clapperboard,
  ClipboardList,
  Eye,
  FileText,
  Film,
  Flag,
  Info,
  ListChecks,
  ListVideo,
  Loader2,
  type LucideIcon,
  MessageCircleQuestion,
  Radar,
  Scissors,
  SunMedium,
  Sparkles,
} from "lucide-react";
import * as React from "react";
import { cn } from "@/lib/utils";
import type { RunStep, StepKind } from "./run-steps";

const ICONS: Readonly<Record<StepKind, LucideIcon>> = {
  brief: FileText,
  footage: Film,
  find_moments: Radar,
  view_clip: Eye,
  plan: ClipboardList,
  timeline: ListVideo,
  edit: Scissors,
  match_exposure: SunMedium,
  preview: Clapperboard,
  check: ListChecks,
  finish: Flag,
  question: MessageCircleQuestion,
  working: Sparkles,
  note: Info,
  error: CircleAlert,
};

function StepIcon({ step }: { step: RunStep }) {
  const Icon = ICONS[step.kind];
  if (step.pending) return <Loader2 aria-hidden className="size-3.5 text-brand motion-safe:animate-spin" />;
  return <Icon aria-hidden className={cn("size-3.5", step.tone === "error" ? "text-critical" : step.tone === "note" ? "text-warning" : "text-muted-foreground")} />;
}

/**
 * The steps of a run as a timeline, one icon per kind of step (looking, planning, editing, previewing, checking, finishing). The
 * newest step is the one the editor is on while the run is active: its tool has not answered yet and it spins.
 */
export function RunStepList({ steps, active, className }: { steps: readonly RunStep[]; active: boolean; className?: string }) {
  const listRef = React.useRef<HTMLOListElement>(null);
  const last = steps.at(-1)?.seq;
  // A live feed stays on its newest line.
  // biome-ignore lint/correctness/useExhaustiveDependencies: scrolls when a step is added.
  React.useEffect(() => {
    const el = listRef.current;
    if (active && el) el.scrollTop = el.scrollHeight;
  }, [last, active]);

  return (
    <ol ref={listRef} aria-label="Steps" className={cn("max-h-72 overflow-y-auto overscroll-contain pr-1", className)}>
      {steps.map((step, index) => (
        <li key={step.seq} data-kind={step.kind} data-tone={step.tone} data-pending={step.pending ? "true" : undefined} className="relative flex gap-2.5 pb-2 last:pb-0">
          {index < steps.length - 1 ? <span aria-hidden className="absolute top-6 bottom-0 left-[11px] w-px bg-hairline" /> : null}
          <span className={cn("relative z-10 mt-0.5 flex size-[22px] shrink-0 items-center justify-center rounded-full bg-surface-raised shadow-raised", step.pending && "ring-2 ring-brand/30")}>
            <StepIcon step={step} />
          </span>
          <p className={cn("min-w-0 pt-[3px] text-[13px] leading-[18px]", step.tone === "error" ? "text-destructive" : step.tone === "note" ? "text-muted-foreground italic" : "text-foreground")}>
            {step.label}
            {step.detail ? <span className="ml-1.5 text-muted-foreground tabular-nums">· {step.detail}</span> : null}
          </p>
        </li>
      ))}
    </ol>
  );
}
