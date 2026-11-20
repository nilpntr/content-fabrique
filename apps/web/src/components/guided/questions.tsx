import { Loader2, Sparkles } from "lucide-react";
import type * as React from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { BackButton, BigButton, CARD, Choice, Problem, Screen, ScreenTitle } from "./ui";
import { type Answers, clipCountWords, kindOf, lengthOf, styleOf, usualStyleHint } from "./words";

export interface QuestionOption<Id extends string> {
  id: Id;
  label: string;
  hint?: string;
}

/** Screens 2a to 2c: one question, its answers as big buttons. A tap answers and moves on. */
export function QuestionStep<Id extends string>({
  number,
  of,
  title,
  options,
  picked,
  onPick,
  onBack,
}: {
  number: number;
  of: number;
  title: string;
  options: readonly QuestionOption<Id>[];
  picked: Id | undefined;
  onPick: (id: Id) => void;
  onBack: () => void;
}) {
  return (
    <Screen>
      <BackButton onClick={onBack} />
      <ScreenTitle eyebrow={`Question ${number} of ${of}`} title={title} />
      <fieldset className="min-w-0 space-y-3">
        <legend className="sr-only">{title}</legend>
        {options.map((option) => (
          <Choice key={option.id} label={option.label} hint={option.hint} picked={picked === option.id} onPick={() => onPick(option.id)} />
        ))}
      </fieldset>
    </Screen>
  );
}

function SummaryRow({ label, value, onChange }: { label: string; value: string; onChange: () => void }) {
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <p className="text-[13px] text-muted-foreground">{label}</p>
        <p className="truncate text-[16px] font-medium">{value}</p>
      </div>
      <Button variant="ghost" size="lg" className="h-11" aria-label={`Change ${label.toLowerCase()}`} onClick={onChange}>
        Change
      </Button>
    </li>
  );
}

export type ReadyStatus =
  | { kind: "idle" }
  /** "Make my video" was tapped; the clips are still on their way. */
  | { kind: "waiting"; ready: number; total: number }
  | { kind: "starting" }
  /** A clip cannot be used: the creator has to look at the clips again. */
  | { kind: "clip_problem" }
  | { kind: "error"; message: string };

/** Screen 3: what was chosen, what it uses, and the one button that starts it. */
export function ReadyStep({
  answers,
  clips,
  song,
  learntStyle,
  status,
  costNote,
  onStart,
  onStopWaiting,
  onEdit,
  onBack,
}: {
  answers: Answers;
  clips: number;
  song: boolean;
  /** The creator has a style the editor learnt from their own videos. */
  learntStyle: boolean;
  status: ReadyStatus;
  costNote: string;
  onStart: () => void;
  /** Takes back a "Make my video" that is still waiting for the clips. */
  onStopWaiting: () => void;
  onEdit: (step: "clips" | "kind" | "length" | "style") => void;
  onBack: () => void;
}) {
  const busy = status.kind === "waiting" || status.kind === "starting";
  const style = styleOf(answers.style);
  let action: React.ReactNode;
  if (status.kind === "clip_problem") {
    action = (
      <Problem title="One of your clips needs a look" message="It didn't upload, or I couldn't read it. Fix it or remove it, then come back.">
        <BigButton onClick={() => onEdit("clips")}>Back to your clips</BigButton>
      </Problem>
    );
  } else {
    action = (
      <div className="space-y-3">
        {status.kind === "error" ? <Problem title="I couldn't start your video" message={status.message} /> : null}
        {status.kind === "waiting" ? (
          <p role="status" className={cn(CARD, "flex items-start gap-3 p-4 text-[15px] text-pretty")}>
            <Loader2 aria-hidden className="mt-0.5 size-5 shrink-0 text-brand motion-safe:animate-spin" />
            <span>
              <span className="font-medium">
                Getting your clips in… {status.ready} of {status.total} {status.ready === 1 ? "is" : "are"} ready.
              </span>{" "}
              <span className="text-muted-foreground">Keep this page open. I'll start as soon as they're in.</span>
            </span>
          </p>
        ) : null}
        <BigButton disabled={busy} onClick={onStart}>
          <Sparkles aria-hidden />
          {status.kind === "starting" ? "Starting…" : status.kind === "waiting" ? "Waiting for your clips…" : status.kind === "error" ? "Try again" : "Make my video"}
        </BigButton>
        {status.kind === "waiting" ? (
          // Nothing has started yet: the creator can still change their mind (drop a slow clip, change an answer).
          <BigButton variant="secondary" onClick={onStopWaiting}>
            Not yet, let me change something
          </BigButton>
        ) : null}
        <p className="text-center text-[13.5px] text-muted-foreground text-pretty">{costNote}</p>
      </div>
    );
  }
  return (
    <Screen>
      {busy ? null : <BackButton onClick={onBack} />}
      <ScreenTitle title="Ready to make your video?" />
      <ul aria-label="Your choices" className={cn(CARD, "divide-y divide-hairline", busy && "pointer-events-none opacity-60")} inert={busy}>
        <SummaryRow label="Clips" value={clipCountWords(clips, song)} onChange={() => onEdit("clips")} />
        <SummaryRow label="Kind of video" value={kindOf(answers.kind).label} onChange={() => onEdit("kind")} />
        <SummaryRow label="Length" value={`${lengthOf(answers.length).label} · ${lengthOf(answers.length).hint}`} onChange={() => onEdit("length")} />
        <SummaryRow label="Style" value={style.id === "mine" ? `${style.label} · ${usualStyleHint(learntStyle)}` : style.label} onChange={() => onEdit("style")} />
      </ul>
      {action}
    </Screen>
  );
}
