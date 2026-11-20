import { Ban, Hash, Loader2, TimerReset, Undo2 } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { ApiError, type ClipFeedback, type Shot } from "@/lib/api/client";
import { useClipFeedbackActions, useShotList } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { feedbackWords, SHOTS_HINT, START_LATER_SECONDS, shotLine } from "./shot-words";

/**
 * The shot list of a proof edit (docs/design/proof-edit-v1.md): every shot with the number the video shows at its top right, its clip
 * and the second it starts at, and two answers per shot: "Never use again" and "Start later". The answer is kept for the clip and the
 * second, so it holds in every video from now on, and can be taken back. Colour-blind safe: the two kinds of feedback differ by icon and
 * words, in blue and orange, never red and green.
 */
export function ShotListCard({ projectId, version, className }: { projectId: string; version: number; className?: string }) {
  const shots = useShotList(projectId, version);
  const { give, remove } = useClipFeedbackActions();
  const headingId = React.useId();
  const error = give.error ?? remove.error;
  return (
    <section aria-labelledby={headingId} data-testid="shot-list" className={cn("space-y-2.5", className)}>
      <h2 id={headingId} className="flex items-center gap-2 text-[15px] font-semibold tracking-[-0.01em]">
        <Hash aria-hidden className="size-4 text-brand" />
        Shots
      </h2>
      <p className="text-[12.5px] text-muted-foreground text-pretty">{SHOTS_HINT}</p>
      {shots.isPending ? (
        <p role="status" className="flex items-center gap-2 text-[13px] text-muted-foreground">
          <Loader2 aria-hidden className="size-3.5 motion-safe:animate-spin" />
          Loading the shots…
        </p>
      ) : shots.isError ? (
        <p role="alert" className="text-[13px] text-muted-foreground">
          The shots could not be loaded.{" "}
          <Button size="sm" variant="link" onClick={() => void shots.refetch()}>
            Try again
          </Button>
        </p>
      ) : (
        <ol className="divide-y divide-hairline rounded-xl bg-well shadow-well">
          {shots.data.shots.map((shot) => (
            <ShotRow
              key={shot.number}
              shot={shot}
              busy={give.isPending || remove.isPending}
              onNever={() => give.mutate({ project_id: projectId, timeline_version: version, shot_number: shot.number, kind: "never_use" })}
              onLater={(seconds) => give.mutate({ project_id: projectId, timeline_version: version, shot_number: shot.number, kind: "start_later", offset_ms: seconds * 1000 })}
              onUndo={(feedback) => remove.mutate(feedback.id)}
            />
          ))}
        </ol>
      )}
      {error ? (
        <p role="alert" className="text-[13px] text-foreground text-pretty">
          {error instanceof ApiError ? error.message : "That did not work. Try again."}
        </p>
      ) : null}
    </section>
  );
}

function ShotRow({
  shot,
  busy,
  onNever,
  onLater,
  onUndo,
}: {
  shot: Shot;
  busy: boolean;
  onNever: () => void;
  onLater: (seconds: number) => void;
  onUndo: (feedback: ClipFeedback) => void;
}) {
  const laterId = React.useId();
  const never = shot.feedback.some((f) => f.kind === "never_use");
  return (
    <li data-testid="shot" className="space-y-1.5 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-surface-raised text-[12.5px] font-semibold tabular-nums shadow-raised">
          <span className="sr-only">Shot </span>
          {shot.number}
        </span>
        <span className="min-w-0 flex-1 truncate text-[13.5px]">{shotLine(shot)}</span>
        <div className="flex items-center gap-1.5">
          <Button size="sm" variant="outline" disabled={busy || never} onClick={onNever}>
            <Ban aria-hidden />
            Never use again
          </Button>
          <label htmlFor={laterId} className="sr-only">
            Start shot {shot.number} later
          </label>
          <select
            id={laterId}
            disabled={busy || never}
            value=""
            onChange={(event) => {
              const seconds = Number(event.target.value);
              if (seconds > 0) onLater(seconds);
            }}
            className="h-7 rounded-[8px] bg-surface-raised px-2 text-[13px] shadow-raised disabled:opacity-50"
          >
            <option value="">Start later…</option>
            {START_LATER_SECONDS.map((s) => (
              <option key={s} value={s}>
                {s} s later
              </option>
            ))}
          </select>
        </div>
      </div>
      {shot.feedback.length > 0 ? (
        <ul className="flex flex-wrap gap-1.5 pl-9">
          {shot.feedback.map((f) => (
            <li
              key={f.id}
              data-kind={f.kind}
              className={cn(
                "flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[12px] font-medium",
                // Orange for "never", blue for "later": the icon and the words say it too.
                f.kind === "never_use" ? "bg-serious/15 text-foreground" : "bg-brand-wash text-foreground",
              )}
            >
              {f.kind === "never_use" ? <Ban aria-hidden className="size-3 text-serious" /> : <TimerReset aria-hidden className="size-3 text-brand" />}
              {feedbackWords(f)}
              <button type="button" disabled={busy} onClick={() => onUndo(f)} className="ml-0.5 inline-flex items-center gap-0.5 text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">
                <Undo2 aria-hidden className="size-3" />
                Undo
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}
