import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { FormatBadge, StatusBadge } from "@/components/project/badges";
import { type CommentsStore, sessionCommentsStore } from "@/components/project/comments-store";
import { EditAgain } from "@/components/project/edit-again";
import { chooseRun, isRunId, useRememberedRun } from "@/components/project/last-run";
import { ProjectReview } from "@/components/project/project-review";
import { relativeTime } from "@/components/project/relative-time";
import { RunPanel } from "@/components/project/run-panel";
import { isRunActive, useProject, useRun, useTimelineVersions } from "@/lib/api/project-hooks";
import { ApiError, type Project } from "@/lib/api/client";
import { Skeleton } from "@/components/ui/skeleton";
import { buttonVariants } from "@/components/ui/button";

/**
 * A project: the editor run (live while it works), the review of the cut and its versions, and the way to edit again.
 * `runId` is the run named in the URL; without one the run started from this browser, or the one that made the latest version, is shown.
 */
export function ProjectPage({
  projectId,
  runId: urlRunId,
  onRunStarted,
  comments = sessionCommentsStore,
}: {
  projectId: string;
  runId?: string;
  /** Called with the run an "Edit again" started, so that the URL can name it. */
  onRunStarted: (runId: string) => void;
  comments?: CommentsStore;
}) {
  const project = useProject(projectId);
  const versions = useTimelineVersions(projectId);

  // Newest first: the first version made by a run. See last-run.ts for the order and for why this is a stand-in.
  const newestByRun = versions.data?.find((version) => version.author_kind === "agent" && isRunId(version.author_id));
  const remembered = useRememberedRun(projectId);
  const runId = chooseRun(urlRunId, remembered, newestByRun ? { runId: newestByRun.author_id, createdAt: newestByRun.created_at } : undefined);
  // The run is read here as well as in its panel (same query, so one request) to know whether the editor is busy.
  const run = useRun(runId);
  const runActive = isRunActive(run.data?.status);

  if (project.isError) {
    const missing = project.error instanceof ApiError && project.error.status === 404;
    return (
      <div className="space-y-4">
        <BackLink />
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well py-10 text-center shadow-well">
          <p className="font-medium">{missing ? "We couldn't find this project" : "The project could not be loaded"}</p>
          <p className="text-muted-foreground">{missing ? "It may have been deleted, or it belongs to another workspace." : "Check your connection and try again."}</p>
          {missing ? null : (
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
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-96 w-full rounded-xl" />
        <p role="status" className="sr-only">
          Loading the project
        </p>
      </div>
    );
  }

  const data = project.data;
  const assetTitles = new Map(data.assets.map((asset) => [asset.asset_id as string, asset.title]));
  const versionList = versions.data ?? [];

  return (
    <div className="space-y-6">
      <BackLink />
      <ProjectHeader project={data} />

      {runId ? <RunPanel key={runId} runId={runId} assetTitles={assetTitles} className="animate-rise" /> : null}

      {versions.isError ? (
        <p role="alert" className="rounded-xl bg-del-wash px-4 py-3 text-destructive">
          The versions could not be loaded. Reload the page to try again.
        </p>
      ) : versions.isPending ? (
        <div aria-busy="true" className="space-y-3">
          <Skeleton className="h-96 w-full rounded-xl" />
        </div>
      ) : (
        <ProjectReview project={data} versions={versionList} comments={comments} runActive={runActive} />
      )}

      <EditAgain projectId={projectId} busy={runActive} firstCut={versionList.length === 0} onStarted={onRunStarted} />

      <ProjectDetails project={data} />
    </div>
  );
}

function BackLink() {
  return (
    <Link to="/projects" className="inline-flex items-center gap-1.5 text-[13.5px] text-muted-foreground transition-colors hover:text-foreground">
      <ArrowLeft aria-hidden className="size-4" />
      All projects
    </Link>
  );
}

function ProjectHeader({ project }: { project: Project }) {
  return (
    <div className="animate-rise space-y-1.5">
      <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">{project.title}</h1>
      <p className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-muted-foreground">
        <FormatBadge format={project.format} />
        <StatusBadge status={project.status} />
        <span className="tabular-nums">
          {project.asset_count} {project.asset_count === 1 ? "clip" : "clips"}
        </span>
        <span aria-hidden>·</span>
        <span>Edited {relativeTime(project.updated_at)}</span>
      </p>
    </div>
  );
}

function ProjectDetails({ project }: { project: Project }) {
  return (
    <details className="group rounded-xl bg-surface-raised text-[13.5px] shadow-soft ring-1 ring-border">
      <summary className="cursor-pointer px-4 py-3 font-medium outline-none select-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset">
        Brief and footage
      </summary>
      <div className="space-y-4 border-t border-hairline px-4 py-3">
        <div>
          <h3 className="text-muted-foreground">Instructions</h3>
          {project.brief.trim() === "" ? <p className="text-muted-foreground">None.</p> : <p className="max-w-prose whitespace-pre-line">{project.brief}</p>}
        </div>
        <div>
          <h3 className="text-muted-foreground">Footage</h3>
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
      </div>
    </details>
  );
}
