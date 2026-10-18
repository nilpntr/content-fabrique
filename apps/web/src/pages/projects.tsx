import { Link } from "@tanstack/react-router";
import { Clapperboard, PlusIcon } from "lucide-react";
import { PageHeader } from "@/components/page";
import { PROJECT_GRID, ProjectCard, usePosters } from "@/components/project/project-card";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/api/project-hooks";

/** The workspace's projects as cards, newest first, each with a frame from its footage and where it is. With none, a way to start one. */
export function ProjectsPage() {
  const projects = useProjects();
  const posters = usePosters();
  const items = projects.data ?? [];
  // Pending includes "not asked yet" (the session is still loading): that is loading, not an empty list.
  const loading = projects.isPending;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Projects"
        description="Every video you've started. Open one to watch it, change it or download it."
        actions={
          <Link to="/projects/new" className={buttonVariants({ size: "md" })}>
            <PlusIcon aria-hidden />
            New video
          </Link>
        }
      />

      {projects.isError ? (
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well py-10 text-center shadow-well">
          <p className="font-medium">Your projects could not be loaded</p>
          <p className="text-muted-foreground">Check your connection and try again.</p>
          <Button variant="secondary" size="sm" onClick={() => void projects.refetch()}>
            Try again
          </Button>
        </div>
      ) : loading ? (
        <ul aria-busy="true" aria-label="Loading your projects" className={PROJECT_GRID}>
          {[0, 1, 2, 3].map((n) => (
            <li key={n}>
              <Skeleton className="aspect-[4/5] w-full rounded-2xl" />
            </li>
          ))}
        </ul>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-2xl bg-well px-6 py-16 text-center shadow-well">
          <span aria-hidden className="flex size-10 items-center justify-center rounded-xl bg-surface-raised text-muted-foreground shadow-raised">
            <Clapperboard className="size-5" />
          </span>
          <p className="font-medium">No projects yet</p>
          <p className="max-w-sm text-muted-foreground text-pretty">Pick a few of your clips and I'll make the first cut. You can change anything afterwards.</p>
          <Link to="/projects/new" className={buttonVariants({ size: "md" })}>
            Make your first video
          </Link>
        </div>
      ) : (
        <>
          <ul className={PROJECT_GRID}>
            {items.map((project) => (
              <li key={project.id}>
                <ProjectCard project={project} posters={posters} />
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
