import { Check, CircleAlert, Download, Loader2, Sparkles, Wand2, Wrench } from "lucide-react";
import * as React from "react";
import { Button, buttonVariants } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Textarea } from "@/components/ui/textarea";
import { useFinalRender, useStartEdit } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { renderFailureMessage, renderMessage, startEditMessage } from "./errors";
import type { ReviewSummary } from "./plain-words";
import { summaryBlocks } from "./run-summary";

/** The longest instruction the API takes. */
export const INSTRUCTIONS_MAX_LENGTH = 2000;

const SECTION = "space-y-2.5";
const HEADING = "flex items-center gap-2 text-[15px] font-semibold tracking-[-0.01em]";

/** "What I made": the editor's own note about the version, as paragraphs and lists. It is text a model wrote; React escapes it. */
export function WhatIMade({ text, className }: { text: string; className?: string }) {
  const blocks = React.useMemo(() => summaryBlocks(text), [text]);
  if (blocks.length === 0) return null;
  return (
    <section aria-label="What I made" data-testid="what-i-made" className={cn(SECTION, className)}>
      <h2 className={HEADING}>
        <Sparkles aria-hidden className="size-4 text-brand" />
        What I made
      </h2>
      <div className="max-w-prose space-y-1.5 text-[14px] leading-[1.55] text-pretty">
        {blocks.map((block, index) =>
          block.kind === "p" ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: the blocks are made from the text and never reorder.
            <p key={index}>{block.text}</p>
          ) : (
            // biome-ignore lint/suspicious/noArrayIndexKey: as above.
            <ul key={index} className="list-disc space-y-0.5 pl-5 marker:text-subtle-foreground">
              {block.items.map((item, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: as above.
                <li key={i}>{item}</li>
              ))}
            </ul>
          ),
        )}
      </div>
    </section>
  );
}

const REVIEW_ICON = { good: Check, fixed: Wrench, not_yet: CircleAlert } as const;

/** The self-review in one line ("I checked 13 things — all good"), and what was checked one click away, in plain names. */
export function SelfReviewLine({ summary, className }: { summary: ReviewSummary; className?: string }) {
  const Icon = REVIEW_ICON[summary.tone];
  return (
    <div data-testid="self-review" data-tone={summary.tone} className={cn("rounded-xl bg-well px-3 py-2.5 shadow-well", className)}>
      <p className="flex items-center gap-2 text-[13.5px] font-medium">
        <span aria-hidden className={cn("flex size-5 shrink-0 items-center justify-center rounded-full", summary.tone === "not_yet" ? "bg-warning/20 text-foreground" : "bg-add-wash text-good")}>
          <Icon strokeWidth={2.5} className="size-3" />
        </span>
        {summary.line}
      </p>
      <Disclosure summary="See what I checked" className="mt-1 pl-7" bodyClassName="pt-1.5">
        <ul aria-label="What I checked" className="grid gap-x-4 gap-y-1 text-[13px] sm:grid-cols-2">
          {summary.items.map((item) => (
            <li key={item.name} className="flex items-center gap-1.5">
              {item.state === "not_yet" ? <CircleAlert aria-hidden className="size-3.5 shrink-0 text-warning" /> : <Check aria-hidden strokeWidth={2.5} className="size-3.5 shrink-0 text-good" />}
              <span className="min-w-0 truncate">{item.name}</span>
              {item.state === "fixed" ? <span className="rounded-full bg-add-wash px-1.5 text-[11px] font-medium text-good">fixed</span> : null}
              {item.state === "not_yet" ? <span className="text-[11.5px] text-muted-foreground">could be better</span> : null}
            </li>
          ))}
        </ul>
      </Disclosure>
    </div>
  );
}

/**
 * Download the version on screen: the full-size video is made on request (it costs no credits), with its progress, and then it is a
 * link to the file. Nothing renders when the API cannot make one.
 */
export function DownloadButton({ projectId, version, versionLabel, className }: { projectId: string; version: number; versionLabel: string; className?: string }) {
  const final = useFinalRender(projectId, version);
  const { state } = final;
  if (state.kind === "unavailable") return null;
  const error = final.requestError ? renderMessage(final.requestError) : state.kind === "failed" ? `The video could not be made: ${renderFailureMessage(state.render.failure_reason)}. Try again.` : undefined;
  return (
    <div className={cn("space-y-1.5", className)} data-testid="download">
      {state.kind === "ready" ? (
        <a href={state.url} download target="_blank" rel="noreferrer" className={cn(buttonVariants({ size: "lg" }), "w-full")}>
          <Download aria-hidden />
          Download {versionLabel}
        </a>
      ) : state.kind === "working" ? (
        <div role="status" className="space-y-1.5 rounded-xl bg-well px-3 py-2.5 shadow-well">
          <p className="flex items-center gap-2 text-[13.5px] font-medium">
            <Loader2 aria-hidden className="size-4 text-brand motion-safe:animate-spin" />
            Getting your video ready… <span className="tabular-nums text-muted-foreground">{state.progress}%</span>
          </p>
          <div role="progressbar" aria-label="Getting your video ready" aria-valuemin={0} aria-valuemax={100} aria-valuenow={state.progress} className="h-1.5 overflow-hidden rounded-full bg-border">
            <div className="h-full rounded-full bg-brand transition-[width] duration-(--dur-slow)" style={{ width: `${state.progress}%` }} />
          </div>
        </div>
      ) : (
        <Button size="lg" className="w-full" onClick={final.requestRender} disabled={final.requesting || state.kind === "loading"}>
          <Download aria-hidden />
          {final.requesting ? "Starting…" : `Download ${versionLabel}`}
        </Button>
      )}
      {error ? (
        <p role="alert" className="text-[13px] text-destructive text-pretty">
          {error}
        </p>
      ) : state.kind === "none" ? (
        <p className="text-[12.5px] text-subtle-foreground">Full quality, ready to post. Free, it doesn't use a video.</p>
      ) : null}
    </div>
  );
}

/**
 * "Tell me what to change": the creator's words start the next edit, which makes a new version (the current one stays). Empty is
 * fine: the editor then looks at its own cut again, and at the comments on the video.
 */
export function FeedbackBox({
  projectId,
  busy = false,
  firstCut = false,
  openComments = 0,
  onStarted,
  className,
}: {
  projectId: string;
  /** An edit is going on: the server would refuse another one. */
  busy?: boolean;
  /** The project has no video yet: the button makes the first one. */
  firstCut?: boolean;
  /** Comments on the video that are not resolved: the editor reads them too. */
  openComments?: number;
  onStarted: (runId: string) => void;
  className?: string;
}) {
  const [text, setText] = React.useState("");
  const start = useStartEdit();
  const id = React.useId();
  const hintId = React.useId();

  function submit(event: React.FormEvent) {
    event.preventDefault();
    if (start.isPending || busy) return;
    const instructions = text.trim();
    start.mutate(
      { projectId, body: instructions === "" ? {} : { instructions } },
      {
        onSuccess: (run) => {
          setText("");
          onStarted(run.id);
        },
      },
    );
  }

  const label = firstCut ? "Make my video" : "Make the changes";
  return (
    <form onSubmit={submit} aria-label={firstCut ? "Make my video" : "Tell me what to change"} className={cn(SECTION, className)}>
      <label htmlFor={id} className={HEADING}>
        <Wand2 aria-hidden className="size-4 text-brand" />
        {firstCut ? "Anything I should know?" : "Tell me what to change"}
      </label>
      <Textarea
        id={id}
        aria-describedby={hintId}
        rows={3}
        maxLength={INSTRUCTIONS_MAX_LENGTH}
        value={text}
        disabled={start.isPending || busy}
        onChange={(event) => setText(event.target.value)}
        placeholder={firstCut ? "Which clip opens, a title, the pace… or leave it to me." : "For example: make it shorter, open on the lockout, and put the title on the first clip."}
        className="min-h-24 bg-surface-raised"
      />
      <p id={hintId} className="text-[12.5px] text-muted-foreground text-pretty">
        {busy
          ? "I'm still working on this video. You can ask for changes when I'm done."
          : firstCut
            ? "I'll pick the best moments from your clips and cut them to the music."
            : `I'll make a new version and keep this one.${openComments > 0 ? ` I'll also go through your ${openComments === 1 ? "comment" : `${openComments} comments`} on the video.` : ""}`}
      </p>
      {start.isError ? (
        <p role="alert" className="rounded-xl bg-del-wash px-3 py-2 text-[13px] text-destructive">
          {startEditMessage(start.error)}
        </p>
      ) : null}
      <Button type="submit" variant={firstCut ? "default" : "secondary"} className="w-full sm:w-auto" disabled={start.isPending || busy} focusableWhenDisabled>
        {start.isPending ? "Starting…" : label}
      </Button>
    </form>
  );
}
