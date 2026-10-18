import { ChevronRight, CircleAlert, CircleCheck, CircleStop, Loader2, MessageCircleQuestion } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, type Run, type RunArtifact } from "@/lib/api/client";
import { isRunActive, useCancelRun, useReplyToRun, useRunArtifacts, useRunQuery } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { hasArtifactEvents, runArtifacts } from "./run-artifacts";
import { EditChecksCard } from "./run-checks";
import { CreativePlanCard } from "./run-plan-card";
import { RunStepList } from "./run-step-list";
import { budgetLimit, currentActivity, failureMessage, runSteps } from "./run-steps";
import { FinishSummary, RunCostLine, runCreditsOf } from "./run-summary";

/** The longest answer the API takes is 16 KB; the box stops well before. */
const ANSWER_MAX_LENGTH = 4000;

const CARD = "rounded-xl bg-surface-raised shadow-soft ring-1 ring-border";

function replyErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.code === "run_not_waiting") return "The editor is not waiting for an answer any more. It may have been answered already.";
  if (error instanceof ApiError && error.status === 403) return "You can't answer for this workspace.";
  return "The answer could not be sent. Try again.";
}

/**
 * The editor run of a project (P4-06): what it is doing in plain words, what it has cost, how long it has run, a Stop button,
 * the question box when it asks something, and at the end the editor's own summary or why it failed. It follows the run by
 * polling (see `useRun`) and stops asking once the run has finished.
 */
export function RunPanel({ runId, assetTitles, controls = true, className }: { runId: string; assetTitles: ReadonlyMap<string, string>; controls?: boolean; className?: string }) {
  const query = useRunQuery(runId);
  const cancel = useCancelRun();
  const reply = useReplyToRun();
  const run = query.data;
  // The whole plan and self-review: asked for once the run's events show it has some, while its panel is on screen. If the endpoint
  // fails the cards are made from the event summaries.
  const stored = useRunArtifacts(runId, { wanted: run ? hasArtifactEvents(run.events ?? []) : false, status: run?.status });

  if (!run) {
    if (query.isError) {
      return (
        <section aria-label="Editor run" className={cn(CARD, "flex items-start gap-3 p-4", className)}>
          <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-critical" />
          <div className="min-w-0 flex-1">
            <p role="alert" className="font-medium">
              {query.error instanceof ApiError && query.error.status === 404 ? "This editor run could not be found." : "The editor run could not be loaded."}
            </p>
            <Button variant="secondary" size="sm" className="mt-2" onClick={() => void query.refetch()}>
              Try again
            </Button>
          </div>
        </section>
      );
    }
    return (
      <section aria-label="Editor run" aria-busy="true" className={cn(CARD, "flex items-center gap-3 p-4 text-muted-foreground", className)}>
        <Loader2 aria-hidden className="size-4 motion-safe:animate-spin" />
        Loading the editor run…
      </section>
    );
  }

  return <RunView run={run} artifacts={stored.data} assetTitles={assetTitles} controls={controls} className={className} onCancel={() => cancel.mutate({ runId, text: "" })} cancelling={cancel.isPending || cancel.isSuccess} cancelError={cancel.isError} onReply={(text) => reply.mutate({ runId, text })} replying={reply.isPending} replyError={reply.isError ? replyErrorMessage(reply.error) : undefined} />;
}

export interface RunViewProps {
  run: Run;
  /** What the run recorded, whole (`GET /v1/runs/{id}/artifacts`). Without it the plan and the checks come from the event summaries. */
  artifacts?: readonly RunArtifact[] | undefined;
  assetTitles: ReadonlyMap<string, string>;
  onCancel: () => void;
  cancelling?: boolean;
  cancelError?: boolean;
  onReply: (text: string) => void;
  replying?: boolean;
  replyError?: string;
  /**
   * Whether the panel has its own Stop button, answer box and closing summary. Default true. Off where the page shows those in
   * plain words already (the project page keeps the panel in its Details).
   */
  controls?: boolean;
  className?: string;
}

/** The run panel for a run that is already loaded. */
export function RunView({ run, artifacts: stored, assetTitles, onCancel, cancelling = false, cancelError = false, onReply, replying = false, replyError, controls = true, className }: RunViewProps) {
  const active = isRunActive(run.status);
  const working = run.status === "queued" || run.status === "running";
  const steps = React.useMemo(() => runSteps(run.events ?? [], assetTitles, { live: working }), [run.events, assetTitles, working]);
  const activity = currentActivity({ status: run.status, events: run.events ?? [], question: run.question }, assetTitles);
  const artifacts = React.useMemo(() => runArtifacts(run.events ?? [], stored), [run.events, stored]);
  // The steps are open while the editor works and fold away when it is done; the creator's own choice wins.
  const [stepsOpen, setStepsOpen] = React.useState<boolean | undefined>(undefined);
  const titleId = React.useId();

  const heading =
    run.status === "succeeded" ? "The editor finished" : run.status === "failed" ? "The editor stopped" : run.status === "cancelled" ? "You stopped the editor" : run.status === "waiting_input" ? "The editor has a question" : "The editor is working";
  const limit = run.status === "failed" && run.failure_reason === "budget" ? budgetLimit(run.events ?? []) : undefined;

  return (
    <section aria-labelledby={titleId} data-run-status={run.status} className={cn(CARD, "space-y-3 p-4", className)}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <StatusIcon status={run.status} />
        <h2 id={titleId} className="text-[15px] font-semibold tracking-[-0.01em]">
          {heading}
        </h2>
        {active && controls ? (
          <Button variant="secondary" size="sm" className="ml-auto" onClick={onCancel} disabled={cancelling} aria-label={cancelling ? "Stopping the editor" : "Stop the editor"}>
            <CircleStop aria-hidden />
            {cancelling ? "Stopping…" : "Stop"}
          </Button>
        ) : null}
      </div>

      <RunCostLine run={run} credits={runCreditsOf(run)} />

      <p role="status" className={cn("text-[13.5px] text-muted-foreground", !active && "sr-only")}>
        {activity}
      </p>
      {cancelError ? (
        <p role="alert" className="text-[13px] text-destructive">
          The editor could not be stopped. Try again.
        </p>
      ) : null}

      {run.status === "waiting_input" && controls ? <QuestionBox question={run.question ?? ""} onReply={onReply} replying={replying} error={replyError} /> : null}
      {run.status === "waiting_input" && !controls ? <p className="text-[13.5px]">{run.question || "The editor needs more information."}</p> : null}

      {artifacts.plan ? <CreativePlanCard plan={artifacts.plan} assetTitles={assetTitles} className="animate-rise" /> : null}
      {artifacts.rounds.length > 0 ? <EditChecksCard rounds={artifacts.rounds} className="animate-rise" /> : null}

      {run.status === "succeeded" && controls ? <FinishSummary text={run.summary || "The editor made your first cut."} version={run.timeline_version} className="animate-rise" /> : null}
      {run.status === "failed" ? (
        <div role="alert" className="space-y-1 rounded-[10px] bg-del-wash px-3 py-2 text-[13.5px]">
          <p className="font-medium text-destructive">
            {failureMessage(run.failure_reason)}
            {limit ? ` It hit ${limit}.` : ""}
          </p>
          {run.summary ? <p className="whitespace-pre-line text-muted-foreground">{run.summary}</p> : null}
        </div>
      ) : null}
      {run.status === "cancelled" ? <p className="text-[13.5px] text-muted-foreground">What the editor made before you stopped it is kept as versions.</p> : null}

      {steps.length > 0 ? (
        <details
          className="group text-[13px]"
          open={stepsOpen ?? active}
          // The toggle event also follows React's own change of `open`; only a difference from what is shown is the creator's click.
          onToggle={(event) => {
            const now = event.currentTarget.open;
            if (now !== (stepsOpen ?? active)) setStepsOpen(now);
          }}
        >
          <summary
            className="flex cursor-pointer items-center gap-1.5 rounded-md py-0.5 text-muted-foreground outline-none select-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-brand">
            <ChevronRight aria-hidden className="size-3.5 transition-transform group-open:rotate-90" />
            {steps.length} {steps.length === 1 ? "step" : "steps"}
            {active ? " so far" : ""}
          </summary>
          <RunStepList steps={steps} active={active} className="mt-2.5" />
        </details>
      ) : null}
    </section>
  );
}

function StatusIcon({ status }: { status: Run["status"] }) {
  if (status === "succeeded") return <CircleCheck aria-hidden className="size-4 text-good" />;
  if (status === "failed") return <CircleAlert aria-hidden className="size-4 text-critical" />;
  if (status === "cancelled") return <CircleStop aria-hidden className="size-4 text-subtle-foreground" />;
  if (status === "waiting_input") return <MessageCircleQuestion aria-hidden className="size-4 text-brand" />;
  return <Loader2 aria-hidden className="size-4 text-brand motion-safe:animate-spin" />;
}

function QuestionBox({ question, onReply, replying, error }: { question: string; onReply: (text: string) => void; replying: boolean; error?: string }) {
  const [text, setText] = React.useState("");
  const id = React.useId();
  const send = (event: React.FormEvent) => {
    event.preventDefault();
    const answer = text.trim();
    if (answer !== "" && !replying) onReply(answer);
  };
  return (
    <form onSubmit={send} className="space-y-2 rounded-[10px] bg-brand-wash p-3">
      <label htmlFor={id} className="block text-[13.5px] font-medium">
        {question || "The editor needs more information."}
      </label>
      <Textarea id={id} rows={3} maxLength={ANSWER_MAX_LENGTH} value={text} onChange={(event) => setText(event.target.value)} disabled={replying} placeholder="Your answer" className="min-h-20 bg-surface-raised" />
      {error ? (
        <p role="alert" className="text-[13px] text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex justify-end">
        <Button type="submit" size="sm" disabled={text.trim() === "" || replying}>
          {replying ? "Sending…" : "Send answer"}
        </Button>
      </div>
    </form>
  );
}
