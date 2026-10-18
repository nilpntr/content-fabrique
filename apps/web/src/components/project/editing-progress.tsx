import { Check, CircleAlert, CircleStop, MessageCircleQuestion, RotateCcw } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { Run } from "@/lib/api/client";
import { isRunActive } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { canRetry, EDIT_STAGES, editProgress, furthestStageOf, stageState, stoppedMessage } from "./plain-words";

/** The longest answer the API takes is 16 KB; the box stops well before. */
const ANSWER_MAX_LENGTH = 4000;

const CARD = "rounded-2xl bg-surface-raised shadow-soft ring-1 ring-border";

/**
 * The edit while it happens, for a creator: one calm line, the four stages ticked off, and a way to stop. A question from the editor
 * becomes a box to answer it. When an edit stopped or was stopped, it says so in a sentence with one thing to do. Step names, tools,
 * costs and codes are in the page's Details, never here.
 */
export function EditingProgress({
  run,
  onStop,
  stopping = false,
  stopError = false,
  onReply,
  replying = false,
  replyError,
  onRetry,
  retrying = false,
  className,
}: {
  run: Pick<Run, "status" | "events" | "question" | "failure_reason">;
  onStop: () => void;
  stopping?: boolean;
  stopError?: boolean;
  onReply: (text: string) => void;
  replying?: boolean;
  replyError?: string;
  /** Starts the edit again (a failed one). */
  onRetry?: () => void;
  retrying?: boolean;
  className?: string;
}) {
  const titleId = React.useId();
  if (run.status === "failed") {
    return (
      <section aria-labelledby={titleId} data-testid="edit-progress" data-state="failed" className={cn(CARD, "flex flex-wrap items-start gap-3 p-4", className)}>
        <CircleAlert aria-hidden className="mt-0.5 size-5 shrink-0 text-critical" />
        <div className="min-w-0 flex-1 space-y-1">
          <h2 id={titleId} className="font-semibold tracking-[-0.01em]">
            I couldn't finish this edit
          </h2>
          <p role="alert" className="text-[13.5px] text-muted-foreground text-pretty">
            {stoppedMessage(run.failure_reason)} Nothing was charged.
          </p>
        </div>
        {onRetry && canRetry(run.failure_reason) ? (
          <Button onClick={onRetry} disabled={retrying} className="w-full sm:w-auto">
            <RotateCcw aria-hidden />
            {retrying ? "Starting…" : "Try again"}
          </Button>
        ) : null}
      </section>
    );
  }
  if (run.status === "cancelled") {
    return (
      <section aria-labelledby={titleId} data-testid="edit-progress" data-state="cancelled" className={cn(CARD, "flex items-start gap-3 p-4", className)}>
        <CircleStop aria-hidden className="mt-0.5 size-5 shrink-0 text-subtle-foreground" />
        <div className="min-w-0 space-y-1">
          <h2 id={titleId} className="font-semibold tracking-[-0.01em]">
            You stopped this edit
          </h2>
          <p className="text-[13.5px] text-muted-foreground">Anything I'd already made is kept as a version.</p>
        </div>
      </section>
    );
  }
  if (!isRunActive(run.status)) return null;

  const progress = editProgress({ status: run.status, events: run.events ?? [] });
  const furthest = furthestStageOf({ events: run.events ?? [] });
  const asking = run.status === "waiting_input";

  return (
    <section aria-labelledby={titleId} data-testid="edit-progress" data-state={asking ? "question" : "working"} className={cn(CARD, "space-y-4 p-4 sm:p-5", className)}>
      <div className="flex flex-wrap items-start gap-3">
        {asking ? (
          <MessageCircleQuestion aria-hidden className="mt-0.5 size-5 shrink-0 text-brand" />
        ) : (
          <span aria-hidden className="relative mt-1.5 flex size-3 shrink-0">
            <span className="absolute inset-0 rounded-full bg-brand/40 motion-safe:animate-ping" />
            <span className="relative size-3 rounded-full bg-brand" />
          </span>
        )}
        <div className="min-w-0 flex-1">
          <h2 id={titleId} className="font-semibold tracking-[-0.01em]">
            {asking ? "I have a question for you" : "I'm making your video"}
          </h2>
          <p role="status" className="text-[13.5px] text-muted-foreground">
            {asking ? "Answer below and I'll carry on." : progress.headline}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={onStop} disabled={stopping} aria-label={stopping ? "Stopping" : "Stop editing"}>
          <CircleStop aria-hidden />
          {stopping ? "Stopping…" : "Stop"}
        </Button>
      </div>

      {asking ? (
        <QuestionBox question={run.question ?? ""} onReply={onReply} replying={replying} error={replyError} />
      ) : (
        <ol aria-label="Progress" className="grid gap-2 sm:grid-cols-4">
          {EDIT_STAGES.map((stage, index) => {
            const state = stageState(index, progress, furthest);
            return (
              <li
                key={stage.id}
                data-state={state}
                className={cn(
                  "flex items-center gap-2 rounded-xl px-3 py-2 text-[13px] transition-colors duration-(--dur-slow)",
                  state === "current" ? "bg-brand-wash font-medium text-foreground" : state === "done" ? "text-muted-foreground" : "text-subtle-foreground",
                )}
              >
                <span
                  aria-hidden
                  className={cn(
                    "flex size-5 shrink-0 items-center justify-center rounded-full",
                    state === "done" ? "bg-add-wash text-good" : state === "current" ? "bg-brand text-white" : "bg-wash",
                  )}
                >
                  {state === "done" ? <Check strokeWidth={3} className="size-3" /> : state === "current" ? <span className="size-1.5 rounded-full bg-white motion-safe:animate-pulse" /> : null}
                </span>
                {stage.label}
                <span className="sr-only">{state === "done" ? ", done" : state === "current" ? ", now" : ""}</span>
              </li>
            );
          })}
        </ol>
      )}

      {stopError ? (
        <p role="alert" className="text-[13px] text-destructive">
          I couldn't stop. Try again.
        </p>
      ) : null}
      {asking ? null : <p className="text-[12.5px] text-subtle-foreground">You can leave this page. I'll keep going, and your video shows up here when it's ready.</p>}
    </section>
  );
}

/** The editor's question and the box to answer it. */
export function QuestionBox({ question, onReply, replying, error, label = "Send answer" }: { question: string; onReply: (text: string) => void; replying: boolean; error?: string; label?: string }) {
  const [text, setText] = React.useState("");
  const id = React.useId();
  const send = (event: React.FormEvent) => {
    event.preventDefault();
    const answer = text.trim();
    if (answer !== "" && !replying) onReply(answer);
  };
  return (
    <form onSubmit={send} className="space-y-2 rounded-xl bg-brand-wash p-3">
      <label htmlFor={id} className="block text-[13.5px] font-medium text-pretty">
        {question || "I need a bit more to go on."}
      </label>
      <Textarea id={id} rows={3} maxLength={ANSWER_MAX_LENGTH} value={text} onChange={(event) => setText(event.target.value)} disabled={replying} placeholder="Your answer" className="min-h-20 bg-surface-raised" />
      {error ? (
        <p role="alert" className="text-[13px] text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex justify-end">
        <Button type="submit" size="sm" disabled={text.trim() === "" || replying}>
          {replying ? "Sending…" : label}
        </Button>
      </div>
    </form>
  );
}
