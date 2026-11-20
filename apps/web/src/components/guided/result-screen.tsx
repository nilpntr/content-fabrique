import { Link } from "@tanstack/react-router";
import { Check, CircleAlert, Loader2, Pencil, RotateCcw, Sparkles } from "lucide-react";
import * as React from "react";
import { renderFailureMessage, renderMessage, startEditMessage } from "@/components/project/errors";
import { DownloadButton } from "@/components/project/review-aside";
import { buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { usePreviewRender, useStartEdit } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { BackButton, BigButton, CARD, Problem, Screen, ScreenTitle } from "./ui";
import { buildChange, CHANGE_CHIPS, CHANGE_MAX_LENGTH, type ChangeChipId } from "./words";

const FRAME = "mx-auto aspect-[9/16] w-full max-w-72 overflow-hidden rounded-3xl bg-well shadow-well";

/**
 * The video of a version, phone-shaped. It is asked for once when there is none yet, shows how far along it is while it is put
 * together, and says in a sentence when it cannot be shown, with the one button that tries again.
 */
export function VideoPlayer({ projectId, version }: { projectId: string; version: number }) {
  const preview = usePreviewRender(projectId, version);
  const { state, requestRender } = preview;

  // No video of this version exists yet: ask for it, once. (Asking again for one that exists returns that one.)
  const asked = React.useRef<number | undefined>(undefined);
  React.useEffect(() => {
    if (state.kind !== "none" || asked.current === version) return;
    asked.current = version;
    requestRender();
  }, [state.kind, version, requestRender]);

  if (state.kind === "ready") {
    return (
      <div className={FRAME}>
        {/* biome-ignore lint/a11y/useMediaCaption: the creator's own video, shown back to them; its words are burnt in. */}
        <video key={state.mp4Url} src={state.mp4Url} controls playsInline preload="metadata" aria-label="Your video" onError={preview.refreshUrls} className="size-full bg-black object-contain" />
      </div>
    );
  }

  const refused = preview.requestError ? renderMessage(preview.requestError) : undefined;
  const trouble =
    refused ??
    (state.kind === "failed"
      ? `The video could not be shown: ${renderFailureMessage(state.render.failure_reason)}.`
      : state.kind === "error"
        ? "The video could not be loaded. Check your connection."
        : state.kind === "unavailable"
          ? "Videos can't be played here yet. Try again later."
          : undefined);
  if (trouble) {
    return (
      <Problem title="I can't show the video right now" message={trouble}>
        {state.kind === "unavailable" ? null : (
          <BigButton variant="secondary" onClick={requestRender} disabled={preview.requesting}>
            <RotateCcw aria-hidden />
            {preview.requesting ? "Trying…" : "Try again"}
          </BigButton>
        )}
      </Problem>
    );
  }

  const percent = state.kind === "working" ? Math.min(100, Math.max(0, Math.round(state.render.progress))) : undefined;
  return (
    <div className="space-y-3" aria-busy="true">
      <Skeleton className={FRAME} />
      <p role="status" className="flex items-center justify-center gap-2 text-[15px] text-muted-foreground">
        <Loader2 aria-hidden className="size-4 text-brand motion-safe:animate-spin" />
        Putting your video together…{percent === undefined ? null : <span className="tabular-nums">{percent}%</span>}
      </p>
    </div>
  );
}

/** "What's in it": the video as a short numbered list, one plain line a shot. */
export function ShotList({ shots, more = 0 }: { shots: readonly string[]; more?: number }) {
  const titleId = React.useId();
  if (shots.length === 0) return null;
  return (
    <section aria-labelledby={titleId} data-testid="shot-list" className={cn(CARD, "space-y-3 p-5")}>
      <h2 id={titleId} className="text-[17px] font-semibold tracking-[-0.01em]">
        What's in it
      </h2>
      <ol className="space-y-2.5">
        {shots.map((line, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: the list is made from the plan and never reorders.
          <li key={index} className="flex gap-3 text-[15px] leading-snug text-pretty">
            <span aria-hidden className="flex size-6 shrink-0 items-center justify-center rounded-full bg-wash text-[13px] font-semibold tabular-nums">
              {index + 1}
            </span>
            <span className="sr-only">{index + 1}.</span>
            <span className="min-w-0">{line}</span>
          </li>
        ))}
      </ol>
      {more > 0 ? (
        <p className="pl-9 text-[14px] text-muted-foreground">
          …and {more} more {more === 1 ? "moment" : "moments"}
        </p>
      ) : null}
    </section>
  );
}

/** "Change something": quick changes to tap, a box for the creator's own words, and the button that starts the next round. */
function ChangeForm({ projectId, onStarted, onBack }: { projectId: string; onStarted: (runId: string) => void; onBack: () => void }) {
  const [picked, setPicked] = React.useState<ReadonlySet<ChangeChipId>>(new Set());
  const [text, setText] = React.useState("");
  const start = useStartEdit();
  const id = React.useId();
  const change = buildChange(picked, text);

  const toggle = (chip: ChangeChipId) =>
    setPicked((previous) => {
      const next = new Set(previous);
      if (!next.delete(chip)) next.add(chip);
      return next;
    });

  function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!change || start.isPending) return;
    start.mutate({ projectId, body: change }, { onSuccess: (run) => onStarted(run.id) });
  }

  return (
    <form onSubmit={submit} aria-label="Change something" className="space-y-4">
      <BackButton onClick={onBack} />
      <h2 className="text-[19px] font-semibold tracking-[-0.015em]">What should I change?</h2>
      <fieldset className="flex min-w-0 flex-wrap gap-2">
        <legend className="sr-only">Quick changes</legend>
        {CHANGE_CHIPS.map((chip) => {
          const on = picked.has(chip.id);
          return (
            <button
              key={chip.id}
              type="button"
              aria-pressed={on}
              disabled={start.isPending}
              onClick={() => toggle(chip.id)}
              className={cn(
                "inline-flex h-12 cursor-pointer items-center gap-1.5 rounded-full px-4 text-[15px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-brand disabled:opacity-50",
                on ? "bg-brand-wash text-foreground ring-2 ring-brand" : "bg-surface-raised ring-1 ring-border",
              )}
            >
              {on ? <Check aria-hidden strokeWidth={3} className="size-4 text-brand" /> : null}
              {chip.label}
            </button>
          );
        })}
      </fieldset>
      <div className="space-y-1.5">
        <label htmlFor={id} className="block text-[15px] font-medium">
          Or tell me in your own words
        </label>
        <Textarea
          id={id}
          rows={3}
          maxLength={CHANGE_MAX_LENGTH}
          value={text}
          disabled={start.isPending}
          onChange={(event) => setText(event.target.value)}
          placeholder="For example: start with the heaviest lift, and make the title bigger."
          className="min-h-24 bg-surface-raised text-[16px]"
        />
      </div>
      {start.isError ? <Problem title="I couldn't start the changes" message={startEditMessage(start.error)} /> : null}
      <BigButton type="submit" disabled={!change || start.isPending}>
        <Sparkles aria-hidden />
        {start.isPending ? "Starting…" : "Make the changes"}
      </BigButton>
      <p className="text-center text-[13.5px] text-muted-foreground text-pretty">I'll make a new video and keep this one.</p>
    </form>
  );
}

/** "Looks great": the download, and where to go from here. */
function AllDone({ projectId, version, onStartOver, onBack }: { projectId: string; version: number; onStartOver: () => void; onBack: () => void }) {
  return (
    <section aria-label="All done" className="space-y-4">
      <BackButton onClick={onBack} />
      <div className="flex items-center gap-3">
        <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-full bg-brand-wash text-brand">
          <Check strokeWidth={3} className="size-5" />
        </span>
        <h2 className="text-[19px] font-semibold tracking-[-0.015em]">All done. It's yours to post.</h2>
      </div>
      <DownloadButton projectId={projectId} version={version} versionLabel="video" className="[&_a]:h-14 [&_a]:rounded-2xl [&_a]:text-[16px] [&_button]:h-14 [&_button]:rounded-2xl [&_button]:text-[16px]" />
      <BigButton variant="secondary" onClick={onStartOver}>
        <Sparkles aria-hidden />
        Make another video
      </BigButton>
      <Link to="/projects" className={cn(buttonVariants({ variant: "ghost", size: "lg" }), "h-12 w-full text-[15px]")}>
        See all my videos
      </Link>
    </section>
  );
}

/**
 * Screen 5: the video, what is in it, and three things to do: it is good, change something, or start over. A change that could not
 * be made (`notice`) is said above the video it left untouched.
 */
export function ResultScreen({
  projectId,
  version,
  shots,
  moreShots = 0,
  notice,
  onRunStarted,
  onStartOver,
}: {
  projectId: string;
  /** The newest version of the video: the one shown, changed and downloaded. */
  version: number;
  shots: readonly string[];
  moreShots?: number;
  notice?: { title: string; message: string };
  onRunStarted: (runId: string) => void;
  onStartOver: () => void;
}) {
  const [mode, setMode] = React.useState<"choose" | "change" | "done">("choose");
  return (
    <Screen data-testid="result">
      <ScreenTitle title="Here's your video" />
      {notice ? (
        <p role="alert" className={cn(CARD, "flex items-start gap-3 p-4 text-[15px] text-pretty")}>
          <CircleAlert aria-hidden className="mt-0.5 size-5 shrink-0" />
          <span>
            <span className="font-medium">{notice.title}</span> <span className="text-muted-foreground">{notice.message}</span>
          </span>
        </p>
      ) : null}
      <VideoPlayer projectId={projectId} version={version} />
      {mode === "choose" ? (
        <>
          <ShotList shots={shots} more={moreShots} />
          <fieldset className="min-w-0 space-y-3">
            <legend className="sr-only">What next?</legend>
            <BigButton onClick={() => setMode("done")}>
              <Check aria-hidden strokeWidth={2.5} />
              Looks great
            </BigButton>
            <BigButton variant="secondary" onClick={() => setMode("change")}>
              <Pencil aria-hidden />
              Change something
            </BigButton>
            <BigButton variant="outline" onClick={onStartOver}>
              <RotateCcw aria-hidden />
              Start over
            </BigButton>
            <p className="text-center text-[13.5px] text-muted-foreground">Starting over keeps this video saved under Projects.</p>
          </fieldset>
        </>
      ) : mode === "change" ? (
        <ChangeForm projectId={projectId} onStarted={onRunStarted} onBack={() => setMode("choose")} />
      ) : (
        <AllDone projectId={projectId} version={version} onStartOver={onStartOver} onBack={() => setMode("choose")} />
      )}
    </Screen>
  );
}
