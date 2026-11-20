import { Link } from "@tanstack/react-router";
import { RotateCcw, Sparkles } from "lucide-react";
import * as React from "react";
import { MakingScreen } from "@/components/guided/making-screen";
import { ResultScreen } from "@/components/guided/result-screen";
import { BigButton, Problem, Screen } from "@/components/guided/ui";
import { recallVibe } from "@/components/guided/vibe-memory";
import { failedChangeWords, failedWords } from "@/components/guided/words";
import { startEditMessage } from "@/components/project/errors";
import { chooseRun, isRunId, useRememberedRun } from "@/components/project/last-run";
import { canRetry, stoppedMessage } from "@/components/project/plain-words";
import { mainItems, markersOf, specDurationMs, textItems } from "@/components/project/review-data";
import { hasArtifactEvents, runArtifacts } from "@/components/project/run-artifacts";
import { storyboard } from "@/components/project/storyboard";
import { buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, type TimelineVersionSummary } from "@/lib/api/client";
import { isRunActive, useCancelRun, useProject, useReplyToRun, useRun, useRunArtifacts, useStartEdit, useTimelineSpecs, useTimelineVersions } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";

function replyErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.code === "run_not_waiting") return "I'm not waiting for an answer any more. It may have been answered already.";
  if (error instanceof ApiError && error.status === 403) return "Your role in this workspace can't answer. Ask the owner.";
  return "Your answer could not be sent. Try again.";
}

const isMissing = (error: unknown): boolean => error instanceof ApiError && error.status === 404;

function Loading() {
  return (
    <Screen aria-busy="true">
      <Skeleton className="h-9 w-64" />
      <Skeleton className="mx-auto aspect-[9/16] w-full max-w-72 rounded-3xl" />
      <Skeleton className="h-14 w-full rounded-2xl" />
      <Skeleton className="h-14 w-full rounded-2xl" />
      <p role="status" className="sr-only">
        Loading your video
      </p>
    </Screen>
  );
}

function CouldNotLoad({ title = "This video could not be loaded", onRetry }: { title?: string; onRetry: () => void }) {
  return (
    <Screen>
      <Problem title={title} message="Check your connection and try again.">
        <BigButton onClick={onRetry}>
          <RotateCcw aria-hidden />
          Try again
        </BigButton>
      </Problem>
    </Screen>
  );
}

/**
 * The second half of the guided flow (docs/design/guided-flow.md): one video, from "being made" to "here it is". While the editor
 * works it shows the calm progress screen; when there is a video it shows it with the three things to do; and whatever stops on the
 * way is said in a sentence with one button. `runId` is the edit named in the URL; without one, the edit this browser started last,
 * or the one that made the newest video, is followed (as on the project page).
 *
 * "The video" is the newest one that a finished edit made. What an edit left behind before it failed or was stopped is not shown
 * here as the video (it is half-made, and cannot be downloaded); it stays on the project's own page.
 */
export function MakeResultPage({
  projectId,
  runId: urlRunId,
  onRunStarted,
  onStartOver,
}: {
  projectId: string;
  runId?: string;
  /** Called with the edit that was started, so that the URL can name it. */
  onRunStarted: (runId: string) => void;
  /** Back to the first screen, for a new video. */
  onStartOver: () => void;
}) {
  const project = useProject(projectId);
  const versions = useTimelineVersions(projectId);
  const newestByRun = versions.data?.find((version) => version.author_kind === "agent" && isRunId(version.author_id));
  const remembered = useRememberedRun(projectId);
  const runId = chooseRun(urlRunId, remembered, newestByRun ? { runId: newestByRun.author_id, createdAt: newestByRun.created_at } : undefined);
  const run = useRun(runId);
  const status = run.data?.status;
  const stored = useRunArtifacts(runId, { wanted: run.data ? hasArtifactEvents(run.data.events ?? []) : false, status });
  const artifacts = React.useMemo(() => runArtifacts(run.data?.events ?? [], stored.data), [run.data?.events, stored.data]);

  const cancel = useCancelRun();
  const reply = useReplyToRun();
  const start = useStartEdit();

  const unfinished = status === "failed" || status === "cancelled";
  const shown = (versions.data ?? [])
    .filter((version) => !(unfinished && version.author_id === runId))
    .reduce<TimelineVersionSummary | undefined>((best, version) => (best && best.version > version.version ? best : version), undefined);
  const [shownSpec] = useTimelineSpecs(projectId, shown ? [shown] : []);

  if (project.isError) {
    if (!isMissing(project.error)) return <CouldNotLoad onRetry={() => void project.refetch()} />;
    return (
      <Screen>
        <Problem title="We couldn't find this video" message="It may have been deleted, or it belongs to another workspace.">
          <BigButton onClick={onStartOver}>
            <Sparkles aria-hidden />
            Make a new video
          </BigButton>
        </Problem>
      </Screen>
    );
  }
  if (versions.isError) return <CouldNotLoad onRetry={() => void versions.refetch()} />;
  // The edit could not be asked about (it may well still be working): say so, rather than "no video". An edit that does not exist
  // any more is no edit.
  if (runId !== undefined && run.isError && !run.data && !isMissing(run.error)) return <CouldNotLoad title="I couldn't check on your video" onRetry={() => void run.refetch()} />;
  // The edit named by the URL is not known yet: wait for it before deciding between "being made" and "no video".
  if (!project.data || !versions.data || (runId !== undefined && run.isPending)) return <Loading />;

  if (run.data && isRunActive(status)) {
    return (
      <MakingScreen
        run={run.data}
        onStop={() => runId && cancel.mutate({ runId, text: "" })}
        stopping={cancel.isPending || (cancel.isSuccess && cancel.variables?.runId === runId)}
        stopError={cancel.isError && cancel.variables?.runId === runId}
        onReply={(text) => runId && reply.mutate({ runId, text })}
        replying={reply.isPending}
        replyError={reply.isError && reply.variables?.runId === runId ? replyErrorMessage(reply.error) : undefined}
      />
    );
  }

  // The edit has just ended with a video that the list does not have yet (it is being read again): not the old video, not "no video".
  const made = status === "succeeded" ? run.data?.timeline_version : undefined;
  if (typeof made === "number" && !versions.data.some((version) => version.version === made)) return <Loading />;

  const failed = status === "failed";
  const reason = run.data?.failure_reason;

  if (!shown) {
    // The project has the words the video was started with; the style is this browser's to remember.
    const vibe = recallVibe(projectId);
    const makeVideo = () => start.mutate({ projectId, body: vibe ? { vibe } : {} }, { onSuccess: (started) => onRunStarted(started.id) });
    const retryable = !failed || canRetry(reason);
    const title = failed ? "I couldn't finish your video" : status === "cancelled" ? "You stopped this one" : "No video yet";
    const message = failed
      ? failedWords(reason, stoppedMessage(reason), retryable)
      : status === "cancelled"
        ? "Your clips are still here. Press the button and I'll start again."
        : "Your clips are in. Press the button and I'll make your video.";
    return (
      <Screen data-testid="no-video">
        <Problem title={title} message={message}>
          {start.isError ? <p className="text-[15px] font-medium text-pretty">{startEditMessage(start.error)}</p> : null}
          {retryable ? (
            <BigButton onClick={makeVideo} disabled={start.isPending}>
              <Sparkles aria-hidden />
              {start.isPending ? "Starting…" : failed ? "Try again" : "Make my video"}
            </BigButton>
          ) : (
            <BigButton onClick={onStartOver}>
              <RotateCcw aria-hidden />
              Start over
            </BigButton>
          )}
        </Problem>
        {retryable ? (
          <button type="button" onClick={onStartOver} className={cn(buttonVariants({ variant: "ghost", size: "lg" }), "h-12 w-full text-[15px]")}>
            Start over
          </button>
        ) : null}
        <Link to="/projects" className={cn(buttonVariants({ variant: "ghost", size: "lg" }), "h-12 w-full text-[15px]")}>
          See all my videos
        </Link>
      </Screen>
    );
  }

  const spec = shownSpec?.data?.spec;
  const items = spec ? mainItems(spec) : [];
  const assetTitles = new Map(project.data.assets.map((asset) => [asset.asset_id as string, asset.title]));
  // The plan belongs to the edit that made the video on screen; another edit's plan would describe another video.
  const plan = made === shown.version ? artifacts.plan : undefined;
  const board = storyboard({
    plan,
    items,
    texts: spec ? textItems(spec) : [],
    assetTitles,
    musical: spec ? markersOf(spec).length > 0 : false,
    durationMs: items.length > 0 ? specDurationMs(items) : undefined,
    plain: true,
  });
  // A long video's list is cut short: its first shots, then how many more there are (the ending among them).
  const folded = (board?.more ?? 0) > 0;
  const shots = folded ? (board?.cards.slice(0, -1) ?? []) : (board?.cards ?? []);
  // A change that did not go through (an edit that failed after this video was made) leaves the video as it was, and says so.
  const failedChange = failed && run.data !== undefined && Date.parse(run.data.created_at) > Date.parse(shown.created_at);
  const notice = failedChange ? { title: "I couldn't make that change.", message: failedChangeWords(reason, stoppedMessage(reason)) } : undefined;

  return (
    <ResultScreen
      // A newer video starts from the three choices again.
      key={shown.version}
      projectId={projectId}
      version={shown.version}
      shots={shots.map((card) => card.line)}
      moreShots={folded ? (board?.more ?? 0) + 1 : 0}
      notice={notice}
      onRunStarted={onRunStarted}
      onStartOver={onStartOver}
    />
  );
}
