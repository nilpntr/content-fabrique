import { Link } from "@tanstack/react-router";
import { Film, Loader2 } from "lucide-react";
import * as React from "react";
import type { ApiAsset, ProjectSummary } from "@/lib/api/client";
import { useAssets } from "@/lib/api/hooks";
import { useProject } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { type PlainStatus, projectStatusWords, type StatusTone } from "@/lib/words";
import { relativeTime } from "./relative-time";

const TONES: Readonly<Record<StatusTone, string>> = {
  neutral: "bg-wash text-muted-foreground",
  busy: "bg-brand-wash text-brand",
  ready: "bg-add-wash text-good",
  attention: "bg-warning/15 text-foreground",
};

/** A project's state in plain words, as a small pill: "Ready to watch", "Editing…", "Needs your footage". */
export function PlainStatusPill({ status, className }: { status: PlainStatus; className?: string }) {
  return (
    <span data-testid="project-status" data-tone={status.tone} className={cn("inline-flex h-5 items-center gap-1 rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap", TONES[status.tone], className)}>
      {status.tone === "busy" ? <Loader2 aria-hidden className="size-3 motion-safe:animate-spin" /> : null}
      {status.label}
    </span>
  );
}

/**
 * The posters of the library's clips, by id: the first page the app already holds. The list is only read here; it does not poll
 * for clips that are still processing (the library does that).
 */
export function usePosters(): ReadonlyMap<string, string> {
  const assets = useAssets({ isOwned: () => true });
  return React.useMemo(() => new Map((assets.data ?? []).flatMap((asset: ApiAsset) => (asset.derivatives?.poster?.url ? [[asset.id, asset.derivatives.poster.url] as const] : []))), [assets.data]);
}

/** A frame for a project: the poster of its first clip that is still there. Undefined while it is not known, or when there is none. */
export function useProjectPoster(projectId: string, posters: ReadonlyMap<string, string>): string | undefined {
  const project = useProject(projectId);
  const first = project.data?.assets.filter((asset) => asset.available).sort((a, b) => a.position - b.position)[0];
  return first ? posters.get(first.asset_id) : undefined;
}

/** A project as a card: a frame from its footage, its title, where it is, and when it was last touched. */
export function ProjectCard({ project, posters, now }: { project: ProjectSummary; posters: ReadonlyMap<string, string>; now?: number }) {
  const poster = useProjectPoster(project.id, posters);
  const [broken, setBroken] = React.useState(false);
  const status = projectStatusWords(project);
  return (
    <Link
      to="/projects/$projectId"
      params={{ projectId: project.id }}
      className="group flex h-full flex-col overflow-hidden rounded-2xl bg-surface-raised shadow-soft ring-1 ring-border transition-[box-shadow,translate] duration-(--dur-base) ease-out outline-none hover:ring-foreground/25 focus-visible:ring-2 focus-visible:ring-brand motion-safe:hover:-translate-y-0.5"
    >
      <div className="relative aspect-[4/5] overflow-hidden bg-well">
        {poster && !broken ? (
          <img src={poster} alt="" loading="lazy" draggable={false} onError={() => setBroken(true)} className="size-full object-cover transition-transform duration-(--dur-slow) ease-out motion-safe:group-hover:scale-[1.03]" />
        ) : (
          <div aria-hidden className="grid size-full place-items-center bg-[linear-gradient(160deg,var(--wash),var(--well))]">
            <Film className="size-6 text-subtle-foreground" />
          </div>
        )}
      </div>
      <div className="min-w-0 space-y-1.5 p-3">
        <h3 className="truncate text-[14.5px] font-semibold tracking-[-0.01em]">{project.title}</h3>
        <PlainStatusPill status={status} />
        <p className="truncate text-[12.5px] text-subtle-foreground">
          {project.asset_count} {project.asset_count === 1 ? "clip" : "clips"} · {relativeTime(project.updated_at, now)}
        </p>
      </div>
    </Link>
  );
}

/** The grid the project cards sit in: two across on a phone, more as the screen grows. */
export const PROJECT_GRID = "grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 xl:grid-cols-4";
