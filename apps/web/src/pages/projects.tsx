import { Link } from "@tanstack/react-router";
import { FolderOpen, PlusIcon } from "lucide-react";
import { DeviceFrame } from "@/components/device-frame";
import { formatInfo } from "@/components/new-project/formats";
import type { Format } from "@/components/new-project/types";
import { FormatBadge, StatusBadge } from "@/components/project/badges";
import { relativeTime } from "@/components/project/relative-time";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { ProjectSummary } from "@/lib/api/client";
import { useProjects } from "@/lib/api/project-hooks";

/** The workspace's projects as cards, newest first. With none, a way to start one. */
export function ProjectsPage() {
  const projects = useProjects();
  const items = projects.data ?? [];
  // Pending includes "not asked yet" (the session is still loading): that is loading, not an empty list.
  const loading = projects.isPending;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="animate-rise">
          <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">Projects</h1>
          <p className="mt-1.5 max-w-prose text-muted-foreground">Every video you've started, with the latest cut the editor made.</p>
        </div>
        <Link to="/projects/new" className={buttonVariants({ size: "md" })}>
          <PlusIcon aria-hidden />
          New project
        </Link>
      </div>

      {projects.isError ? (
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well py-10 text-center shadow-well">
          <p className="font-medium">Your projects could not be loaded</p>
          <p className="text-muted-foreground">Check your connection and try again.</p>
          <Button variant="secondary" size="sm" onClick={() => void projects.refetch()}>
            Try again
          </Button>
        </div>
      ) : loading ? (
        <ul aria-busy="true" aria-label="Loading your projects" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((n) => (
            <li key={n}>
              <Skeleton className="h-36 w-full rounded-xl" />
            </li>
          ))}
        </ul>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-2xl bg-well py-16 text-center shadow-well">
          <FolderOpen aria-hidden className="size-6 text-muted-foreground" />
          <p className="font-medium">No projects yet</p>
          <p className="max-w-sm text-muted-foreground text-pretty">Pick a format and some of your clips, and the editor makes the first cut.</p>
          <Link to="/projects/new" className={buttonVariants({ size: "md" })}>
            Start your first project
          </Link>
        </div>
      ) : (
        <>
          <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {items.map((project) => (
              <li key={project.id}>
                <ProjectCard project={project} />
              </li>
            ))}
          </ul>
          {projects.hasNextPage ? (
            <div className="flex justify-center">
              <Button variant="secondary" disabled={projects.isFetchingNextPage} onClick={() => void projects.fetchNextPage()}>
                {projects.isFetchingNextPage ? "Loading" : "Show more projects"}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}

function device(format: string) {
  try {
    return formatInfo(format as Format);
  } catch {
    return undefined;
  }
}

function ProjectCard({ project }: { project: ProjectSummary }) {
  const info = device(project.format);
  return (
    <Link
      to="/projects/$projectId"
      params={{ projectId: project.id }}
      className="group flex h-full gap-4 rounded-xl bg-surface-raised p-4 shadow-soft ring-1 ring-border transition-[box-shadow,translate] duration-(--dur-base) ease-out outline-none hover:ring-foreground/25 focus-visible:ring-2 focus-visible:ring-brand motion-safe:hover:-translate-y-0.5"
    >
      <div aria-hidden className="flex w-16 shrink-0 items-center justify-center">
        {info ? <DeviceFrame kind={info.device} postRatio={info.postRatio} size="sm" /> : null}
      </div>
      <div className="min-w-0 flex-1 space-y-2">
        <h2 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{project.title}</h2>
        <p className="flex flex-wrap items-center gap-1.5">
          <FormatBadge format={project.format} />
          <StatusBadge status={project.status} />
        </p>
        <p className="text-[13px] text-muted-foreground">
          <span className="tabular-nums">
            {project.asset_count} {project.asset_count === 1 ? "clip" : "clips"}
          </span>
          {project.current_timeline_version ? <span className="tabular-nums"> · v{project.current_timeline_version}</span> : <span> · no cut yet</span>}
        </p>
        <p className="text-[12.5px] text-subtle-foreground">Edited {relativeTime(project.updated_at)}</p>
      </div>
    </Link>
  );
}
