import { Check, CircleStop, Loader2 } from "lucide-react";
import { QuestionBox } from "@/components/project/editing-progress";
import { Button } from "@/components/ui/button";
import type { Run } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { CARD, Screen, ScreenTitle } from "./ui";
import { MAKING_STEPS, makingProgress, type StepState } from "./words";

const STATE_WORDS: Readonly<Record<StepState, string>> = { done: "Done", now: "Now", todo: "" };

/**
 * Screen 4: the video while it is being made. Five steps in plain words, each with an icon and a word for where it is, lit by the
 * editor's own events; a way to stop; and, when the editor asks something, the question with a box to answer it. Nothing here
 * moves except the one spinner on the step in progress.
 */
export function MakingScreen({
  run,
  onStop,
  stopping = false,
  stopError = false,
  onReply,
  replying = false,
  replyError,
}: {
  run: Pick<Run, "status" | "events" | "question">;
  onStop: () => void;
  stopping?: boolean;
  stopError?: boolean;
  onReply: (text: string) => void;
  replying?: boolean;
  replyError?: string;
}) {
  if (run.status === "waiting_input") {
    return (
      <Screen data-testid="making" data-state="question">
        <ScreenTitle title="I have a question for you" line="Answer below and I'll carry on with your video." />
        <QuestionBox question={run.question ?? ""} onReply={onReply} replying={replying} error={replyError} />
      </Screen>
    );
  }
  const progress = makingProgress(run);
  return (
    <Screen data-testid="making" data-state="working">
      <ScreenTitle title="I'm making your video" line={<span role="status">{progress.headline}</span>} />
      <ol aria-label="Steps" className={cn(CARD, "divide-y divide-hairline")}>
        {MAKING_STEPS.map((step, index) => {
          const state = progress.states[index] as StepState;
          return (
            <li key={step.id} data-state={state} className={cn("flex items-center gap-3 px-4 py-3.5 text-[16px]", state === "todo" ? "text-muted-foreground" : state === "now" ? "font-semibold" : "")}>
              <span aria-hidden className={cn("flex size-7 shrink-0 items-center justify-center rounded-full", state === "todo" ? "border-2 border-dashed border-border" : "bg-brand-wash text-brand")}>
                {state === "done" ? <Check strokeWidth={3} className="size-4" /> : state === "now" ? <Loader2 className="size-4 motion-safe:animate-spin" /> : null}
              </span>
              <span className="min-w-0 flex-1">{step.label}</span>
              {state === "todo" ? null : <span className="text-[13px] font-medium text-muted-foreground">{STATE_WORDS[state]}</span>}
            </li>
          );
        })}
      </ol>
      <p className="text-[15px] text-muted-foreground text-pretty">This takes a few minutes. You can leave this page. Your video will be waiting under Projects.</p>
      {stopError ? (
        <p role="alert" className="text-[15px] font-medium">
          I couldn't stop. Try again.
        </p>
      ) : null}
      <Button variant="secondary" size="lg" className="h-12 rounded-2xl px-5 text-[15px]" onClick={onStop} disabled={stopping}>
        <CircleStop aria-hidden />
        {stopping ? "Stopping…" : "Stop"}
      </Button>
    </Screen>
  );
}
