import { CircleAlert, Clapperboard, Info, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { PreviewRenderState } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { renderFailureMessage } from "./errors";

const STAGES: Readonly<Record<string, string>> = {
  staging: "Getting the footage",
  measuring: "Measuring the sound",
  rendering: "Rendering",
  contact_sheet: "Making a contact sheet",
  uploading: "Saving the video",
};

const BAR = "flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[10px] bg-wash px-3 py-2 text-[12.5px] shadow-[inset_0_0_0_1px_var(--border)]";

/**
 * The line above the player about the preview render of the version on screen: none yet (with the button that asks for one),
 * rendering (with its progress), failed (with a retry), or that the server cannot render yet. A ready preview needs no line:
 * the video is the answer.
 */
export function PreviewStatus({
  state,
  version,
  onRequest,
  requesting,
  requestError,
  className,
}: {
  state: PreviewRenderState;
  version: number;
  onRequest: () => void;
  requesting: boolean;
  /** Why the last request was refused, in words. */
  requestError?: string;
  className?: string;
}) {
  const error = requestError ? (
    <p role="alert" className="flex basis-full items-center gap-1.5 text-destructive">
      <CircleAlert aria-hidden className="size-3.5 shrink-0" />
      {requestError}
    </p>
  ) : null;

  if (state.kind === "ready") return error ? <div className={cn(BAR, className)}>{error}</div> : null;

  if (state.kind === "loading") {
    return (
      <p role="status" className={cn(BAR, "text-muted-foreground", className)}>
        <Loader2 aria-hidden className="size-3.5 motion-safe:animate-spin" />
        Looking for a preview of v{version}…
      </p>
    );
  }

  if (state.kind === "unavailable") {
    return (
      <p role="note" className={cn(BAR, "text-muted-foreground", className)}>
        <Info aria-hidden className="size-3.5 shrink-0 text-subtle-foreground" />
        Video previews aren't available yet. The timeline below shows the cut; the picture arrives with previews.
      </p>
    );
  }

  if (state.kind === "working") {
    const percent = Math.min(100, Math.max(0, Math.round(state.render.progress)));
    const stage = state.render.stage ? (STAGES[state.render.stage] ?? "Rendering") : state.render.status === "queued" ? "Waiting in line" : "Rendering";
    return (
      <div className={cn(BAR, className)}>
        <Loader2 aria-hidden className="size-3.5 text-brand motion-safe:animate-spin" />
        <p role="status" className="text-foreground">
          {stage} a preview of v{version}… <span className="tabular-nums">{percent}%</span>
        </p>
        <div role="progressbar" aria-label={`Preview of v${version}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} className="h-1.5 min-w-24 flex-1 overflow-hidden rounded-full bg-border">
          <div className="h-full rounded-full bg-brand transition-[width] duration-(--dur-slow)" style={{ width: `${percent}%` }} />
        </div>
        {error}
      </div>
    );
  }

  const failed = state.kind === "failed";
  return (
    <div className={cn(BAR, className)}>
      {failed ? <CircleAlert aria-hidden className="size-3.5 shrink-0 text-critical" /> : <Clapperboard aria-hidden className="size-3.5 shrink-0 text-subtle-foreground" />}
      <p role={failed || state.kind === "error" ? "alert" : "note"} className={failed || state.kind === "error" ? "text-destructive" : "text-muted-foreground"}>
        {failed
          ? `The preview of v${version} could not be made: ${renderFailureMessage(state.render.failure_reason)}.`
          : state.kind === "error"
            ? state.message
            : `There is no video preview of v${version} yet.`}
      </p>
      <Button size="sm" variant="secondary" className="ml-auto" onClick={onRequest} disabled={requesting}>
        {requesting ? "Starting…" : failed ? "Try again" : "Render preview"}
      </Button>
      {error}
    </div>
  );
}
