import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import * as React from "react";
import { mainItems, markersOf, specDurationMs, textItems } from "@/components/project/review-data";
import { EditingProgress } from "@/components/project/editing-progress";
import { chooseRun, isRunId, useRememberedRun } from "@/components/project/last-run";
import { reviewSummary } from "@/components/project/plain-words";
import { PlainStatusPill, usePosters } from "@/components/project/project-card";
import { ProjectReview } from "@/components/project/project-review";
import { relativeTime } from "@/components/project/relative-time";
import { DownloadButton, FeedbackBox, SelfReviewLine, WhatIMade } from "@/components/project/review-aside";
import { hasArtifactEvents, runArtifacts } from "@/components/project/run-artifacts";
import { RunPanel } from "@/components/project/run-panel";
import { storyboard } from "@/components/project/storyboard";
import { StoryboardView } from "@/components/project/storyboard-view";
import { buttonVariants } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, type Project, type Run } from "@/lib/api/client";
import { isRunActive, useCancelRun, useProject, useReplyToRun, useRun, useRunArtifacts, useStartEdit, useTimelineSpecs, useTimelineVersions } from "@/lib/api/project-hooks";
import { type PlainStatus, projectStatusWords, versionName } from "@/lib/words";

function replyErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.code === "run_not_waiting") return "I'm not waiting for an answer any more. It may have been answered already.";
  if (error instanceof ApiError && error.status === 403) return "Your role in this workspace can't answer. Ask the owner.";
  return "Your answer could not be sent. Try again.";
}

/**
 * A project, for a creator: the video (big, phone-shaped), what the editor made in a few lines, the box to say what to change, the
 * download, the versions to switch between, and the plan as a storyboard. While the editor works, a calm progress card says where it
 * is. Everything technical (the step list, the checks with their reasons, costs, ids, the effects on the timeline) is behind
 * "Details", closed until opened.
 *
 * `runId` is the run named in the URL; without one the run started from this browser, or the one that made the latest version, is
 * shown.
 */
export function ProjectPage({
  projectId,
  runId: urlRunId,
  onRunStarted,
}: {
  projectId: string;
  runId?: string;
  /** Called with the run an edit started, so that the URL can name it. */
  onRunStarted: (runId: string) => void;
}) {
  const project = useProject(projectId);
  const versions = useTimelineVersions(projectId);
  const [details, setDetails] = React.useState(false);

  // Newest first: the first version made by a run. See last-run.ts for the order and for why this is a stand-in.
  const newestByRun = versions.data?.find((version) => version.author_kind === "agent" && isRunId(version.author_id));
  const remembered = useRememberedRun(projectId);
  const runId = chooseRun(urlRunId, remembered, newestByRun ? { runId: newestByRun.author_id, createdAt: newestByRun.created_at } : undefined);
  const run = useRun(runId);
  const runActive = isRunActive(run.data?.status);
  const stored = useRunArtifacts(runId, { wanted: run.data ? hasArtifactEvents(run.data.events ?? []) : false, status: run.data?.status });
  const artifacts = React.useMemo(() => runArtifacts(run.data?.events ?? [], stored.data), [run.data?.events, stored.data]);

  const cancel = useCancelRun();
  const reply = useReplyToRun();
  const retry = useStartEdit();
  const posters = usePosters();

  // The latest version's spec (shared with the review, one request) tells the storyboard what is on screen when there is no plan.
  const latestSummary = versions.data?.[0];
  const [latestSpec] = useTimelineSpecs(projectId, latestSummary ? [latestSummary] : []);

  if (project.isError) {
    const missing = project.error instanceof ApiError && project.error.status === 404;
    return (
      <div className="space-y-4">
        <BackLink />
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well px-6 py-10 text-center shadow-well">
          <p className="font-medium">{missing ? "We couldn't find this video" : "This video could not be loaded"}</p>
          <p className="text-muted-foreground">{missing ? "It may have been deleted, or it belongs to another workspace." : "Check your connection and try again."}</p>
          {missing ? (
            <Link to="/projects" className={buttonVariants({ variant: "secondary", size: "sm" })}>
              Back to your projects
            </Link>
          ) : (
            <button type="button" className={buttonVariants({ variant: "secondary", size: "sm" })} onClick={() => void project.refetch()}>
              Try again
            </button>
          )}
        </div>
      </div>
    );
  }
  if (!project.data) {
    return (
      <div className="space-y-4" aria-busy="true">
        <BackLink />
        <Skeleton className="h-8 w-72" />
        <div className="grid gap-5 md:grid-cols-[auto_minmax(0,1fr)]">
          <Skeleton className="mx-auto aspect-[9/16] w-full max-w-80 rounded-[22px]" />
          <div className="space-y-3">
            <Skeleton className="h-5 w-40" />
            <Skeleton className="h-20 w-full rounded-xl" />
            <Skeleton className="h-24 w-full rounded-xl" />
          </div>
        </div>
        <p role="status" className="sr-only">
          Loading your video
        </p>
      </div>
    );
  }

  const data = project.data;
  const assetTitles = new Map(data.assets.map((asset) => [asset.asset_id as string, asset.title]));
  const versionList = versions.data ?? [];
  const latest = versionList.reduce((max, version) => Math.max(max, version.version), 0);
  const firstCut = versionList.length === 0;
  const review = run.data?.status === "succeeded" ? reviewSummary(artifacts.rounds) : undefined;

  const spec = latestSpec?.data?.spec;
  const items = spec ? mainItems(spec) : [];
  const board = storyboard({
    plan: artifacts.plan,
    items,
    texts: spec ? textItems(spec) : [],
    assetTitles,
    musical: spec ? markersOf(spec).length > 0 : false,
    durationMs: items.length > 0 ? specDurationMs(items) : undefined,
  });

  const showProgress = run.data !== undefined && (runActive || run.data.status === "failed" || (run.data.status === "cancelled" && isRecent(run.data)));
  const progress = run.data && showProgress ? (
    <EditingProgress
      run={run.data}
      onStop={() => runId && cancel.mutate({ runId, text: "" })}
      stopping={cancel.isPending || (cancel.isSuccess && runActive)}
      stopError={cancel.isError}
      onReply={(text) => runId && reply.mutate({ runId, text })}
      replying={reply.isPending}
      replyError={reply.isError ? replyErrorMessage(reply.error) : undefined}
      onRetry={() => retry.mutate({ projectId, body: {} }, { onSuccess: (started) => onRunStarted(started.id) })}
      retrying={retry.isPending}
      className="animate-rise"
    />
  ) : null;

  const feedback = <FeedbackBox projectId={projectId} busy={runActive} firstCut={firstCut} onStarted={onRunStarted} />;

  return (
    <div className="space-y-6">
      <BackLink />
      <ProjectHeader project={data} status={runActive ? { label: "Editing…", tone: "busy" } : projectStatusWords(data)} />

      {progress}

      {versions.isError ? (
        <p role="alert" className="rounded-xl bg-del-wash px-4 py-3 text-destructive">
          Your video could not be loaded. Reload the page to try again.
        </p>
      ) : versions.isPending ? (
        <div aria-busy="true" className="grid gap-5 md:grid-cols-[auto_minmax(0,1fr)]">
          <Skeleton className="mx-auto aspect-[9/16] w-full max-w-80 rounded-[22px]" />
          <Skeleton className="h-48 w-full rounded-xl" />
        </div>
      ) : (
        <ProjectReview
          project={data}
          versions={versionList}
          runActive={runActive}
          detailed={details}
          empty={runActive ? null : <FirstCut>{feedback}</FirstCut>}
          aside={({ version, openComments }) => {
            // The editor's note belongs to the version its run ended on; another version says what changed in it.
            const note = run.data?.status === "succeeded" && run.data.summary && run.data.timeline_version === version.version ? run.data.summary : undefined;
            return (
              <>
                {note ? <WhatIMade text={note} /> : null}
                {note && review ? <SelfReviewLine summary={review} /> : null}
                {version.loading ? null : <DownloadButton projectId={projectId} version={version.version} versionLabel={version.version === latest ? "video" : versionName(version.version)} />}
                <FeedbackBox projectId={projectId} busy={runActive} openComments={openComments} onStarted={onRunStarted} className="border-t border-hairline pt-4" />
              </>
            );
          }}
        />
      )}

      {board && !firstCut ? <StoryboardView board={board} posters={posters} className="animate-rise" /> : null}

      <Disclosure
        data-testid="project-details"
        summary="Details"
        hint="Steps, checks, costs and the editing timeline"
        onToggle={setDetails}
        className="rounded-2xl bg-well px-4 py-2.5 shadow-well"
        bodyClassName="space-y-4 pt-3 pb-2"
      >
        {details ? (
          <>
            {runId ? <RunPanel key={runId} runId={runId} assetTitles={assetTitles} controls={false} /> : <p className="text-[13.5px] text-muted-foreground">No edit has run on this project yet.</p>}
            <ProjectFacts project={data} runId={runId} run={run.data} />
          </>
        ) : null}
      </Disclosure>
    </div>
  );
}

/** A finished or stopped run is worth a card for a while after it ended; a failed one always (it has the retry). */
function isRecent(run: Pick<Run, "finished_at">): boolean {
  const ended = run.finished_at ? Date.parse(run.finished_at) : Number.NaN;
  return Number.isFinite(ended) && Date.now() - ended < 10 * 60_000;
}

function BackLink() {
  return (
    <Link to="/projects" className="inline-flex items-center gap-1.5 text-[13.5px] text-muted-foreground transition-colors hover:text-foreground">
      <ArrowLeft aria-hidden className="size-4" />
      All projects
    </Link>
  );
}

function ProjectHeader({ project, status }: { project: Project; status: PlainStatus }) {
  return (
    <div className="animate-rise space-y-1.5">
      <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">{project.title}</h1>
      <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-[13.5px] text-muted-foreground">
        <PlainStatusPill status={status} />
        <span className="tabular-nums">
          {project.asset_count} {project.asset_count === 1 ? "clip" : "clips"}
        </span>
        <span aria-hidden>·</span>
        <span>Edited {relativeTime(project.updated_at)}</span>
      </p>
    </div>
  );
}

/** No video yet and nothing running: what happens next, and the box to start it. */
function FirstCut({ children }: { children: React.ReactNode }) {
  return (
    <section aria-label="Your video" className="max-w-xl animate-rise space-y-4 rounded-2xl bg-surface-raised p-4 shadow-soft ring-1 ring-border sm:p-5">
      <div>
        <h2 className="text-[17px] font-semibold tracking-[-0.015em]">No video yet</h2>
        <p className="mt-1 text-[13.5px] text-muted-foreground text-pretty">Your clips are in. Tell me anything you have in mind, or just start, and I'll make the first cut.</p>
      </div>
      {children}
    </section>
  );
}

/** The brief, the footage and the ids, for the curious and for support. */
function ProjectFacts({ project, runId, run }: { project: Project; runId: string | undefined; run: Run | undefined }) {
  return (
    <div className="space-y-4 rounded-xl bg-surface-raised p-4 text-[13.5px] shadow-soft ring-1 ring-border">
      <div>
        <h3 className="text-muted-foreground">Your instructions</h3>
        {project.brief.trim() === "" ? <p className="text-muted-foreground">None.</p> : <p className="max-w-prose whitespace-pre-line">{project.brief}</p>}
      </div>
      <div>
        <h3 className="text-muted-foreground">Clips</h3>
        <ol className="mt-1 space-y-1">
          {project.assets.map((asset) => (
            <li key={asset.asset_id} className="flex items-baseline gap-2">
              <span className="w-6 shrink-0 font-mono text-[12px] text-subtle-foreground tabular-nums">{asset.position + 1}</span>
              <span className="min-w-0 truncate">{asset.title}</span>
              {asset.available ? null : <span className="rounded-full bg-del-wash px-2 text-[11.5px] text-destructive">No longer available</span>}
            </li>
          ))}
        </ol>
      </div>
      <dl className="grid grid-cols-[minmax(6rem,8rem)_1fr] gap-x-4 gap-y-1 font-mono text-[12px] text-muted-foreground">
        <dt>Project</dt>
        <dd className="min-w-0 truncate">{project.id}</dd>
        <dt>Format</dt>
        <dd>{project.format}</dd>
        {runId ? (
          <>
            <dt>Run</dt>
            <dd className="min-w-0 truncate">{runId}</dd>
          </>
        ) : null}
        {run?.failure_reason ? (
          <>
            <dt>Stopped because</dt>
            <dd>{run.failure_reason}</dd>
          </>
        ) : null}
      </dl>
    </div>
  );
}
