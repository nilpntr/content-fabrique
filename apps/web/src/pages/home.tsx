import { useUser } from "@clerk/tanstack-react-start";
import { Link } from "@tanstack/react-router";
import { ArrowRight, CloudUpload, Film, PlusIcon, Sparkles, ThumbsUp } from "lucide-react";
import * as React from "react";
import { PageHeader } from "@/components/page";
import { PROJECT_GRID, ProjectCard, usePosters } from "@/components/project/project-card";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useMe } from "@/lib/api/hooks";
import { useProjects } from "@/lib/api/project-hooks";
import { creditsOf, formatDay } from "@/lib/credits";
import { cn } from "@/lib/utils";
import { greeting, videosLeftText } from "@/lib/words";

/** How many projects "Continue where you left off" shows. */
const RECENT = 4;

/** The greeting depends on the clock, which the server and the browser may not agree on: it is read after hydration. */
function useNow(): Date | undefined {
  const [now, setNow] = React.useState<Date | undefined>(undefined);
  React.useEffect(() => setNow(new Date()), []);
  return now;
}

/**
 * Home: a greeting, "New video", the projects to pick up again (newest change first) and how many videos the credits still
 * make. Everything here is the workspace's own data; a block whose data the API does not have is left out, never made up.
 */
export function HomePage() {
  const { user } = useUser();
  const now = useNow();
  const first = (user as { firstName?: string | null } | null | undefined)?.firstName ?? user?.fullName?.split(" ")[0];
  const projects = useProjects();
  const posters = usePosters();
  const items = projects.data ?? [];
  const recent = [...items].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at)).slice(0, RECENT);

  return (
    <div className="space-y-8">
      <PageHeader
        title={greeting(now ?? new Date(2000, 0, 1, 9), first)}
        description={items.length > 0 ? "Pick up where you left off, or start something new." : "Let's make your first video."}
        actions={
          <Link to="/projects/new" className={buttonVariants({ size: "lg" })}>
            <PlusIcon aria-hidden />
            New video
          </Link>
        }
      />

      <CreditsBlock />

      {projects.isError ? (
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well px-6 py-10 text-center shadow-well">
          <p className="font-medium">Your videos could not be loaded</p>
          <p className="text-muted-foreground">Check your connection and try again.</p>
          <Button variant="secondary" size="sm" onClick={() => void projects.refetch()}>
            Try again
          </Button>
        </div>
      ) : projects.isPending ? (
        <section aria-label="Loading your videos" aria-busy="true" className="space-y-3">
          <Skeleton className="h-5 w-56" />
          <ul className={PROJECT_GRID}>
            {[0, 1, 2, 3].map((n) => (
              <li key={n} className={n > 1 ? "hidden md:block" : undefined}>
                <Skeleton className="aspect-[4/5] w-full rounded-2xl" />
              </li>
            ))}
          </ul>
        </section>
      ) : items.length === 0 ? (
        <FirstVideo />
      ) : (
        <section aria-labelledby="recent-title" className="space-y-3">
          <div className="flex items-baseline justify-between gap-3">
            <h2 id="recent-title" className="text-[17px] font-semibold tracking-[-0.015em]">
              Continue where you left off
            </h2>
            <Link to="/projects" className="inline-flex shrink-0 items-center gap-1 text-[13.5px] text-muted-foreground transition-colors hover:text-foreground">
              All projects
              <ArrowRight aria-hidden className="size-3.5" />
            </Link>
          </div>
          <ul className={PROJECT_GRID}>
            {recent.map((project) => (
              <li key={project.id} className="animate-rise">
                <ProjectCard project={project} posters={posters} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

/** "≈ 12 videos left this month", once the API reports a balance. Nothing renders without one. */
function CreditsBlock() {
  const me = useMe();
  const credits = creditsOf(me.data);
  if (!credits) return null;
  const empty = credits.available === 0;
  return (
    <section
      aria-label="Videos left"
      data-testid="home-credits"
      className={cn("flex animate-rise flex-wrap items-center gap-x-4 gap-y-2 rounded-2xl px-4 py-3.5 shadow-soft ring-1 ring-border", empty || credits.low ? "bg-warning/10" : "bg-surface-raised")}
    >
      <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-brand-wash text-brand">
        <Film className="size-4.5" />
      </span>
      <div className="min-w-0 flex-1">
        <p className="font-semibold tracking-[-0.01em] tabular-nums">{videosLeftText(credits)}</p>
        <p className="text-[13px] text-muted-foreground">
          {credits.period_end && credits.allowance > 0 ? `New videos on ${formatDay(credits.period_end)}. ` : ""}Changes to a video are included.
        </p>
      </div>
      <Link to="/settings" className="text-[13.5px] text-muted-foreground transition-colors hover:text-foreground">
        Your plan
      </Link>
    </section>
  );
}

const STEPS = [
  { icon: CloudUpload, title: "Upload your clips", text: "Straight from your phone: sets, poses, the walk to the rack. No need to trim anything." },
  { icon: Sparkles, title: "I edit them", text: "I pick the best reps, cut them to the music and add a title. It takes a few minutes." },
  { icon: ThumbsUp, title: "You review", text: "Watch it, tell me what to change in your own words, and download it when you love it." },
] as const;

/** A brand-new account: what happens, in three steps, and the two ways to begin. */
function FirstVideo() {
  return (
    <section aria-labelledby="first-video-title" className="animate-rise space-y-5 rounded-2xl bg-surface-raised p-5 shadow-soft ring-1 ring-border sm:p-6">
      <div>
        <h2 id="first-video-title" className="text-[17px] font-semibold tracking-[-0.015em]">
          How it works
        </h2>
        <p className="mt-1 text-muted-foreground">Your first video is three steps away.</p>
      </div>
      <ol className="grid gap-4 md:grid-cols-3">
        {STEPS.map((step, index) => (
          <li key={step.title} className="flex gap-3 rounded-xl bg-well p-3.5 shadow-well md:flex-col">
            <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-surface-raised text-brand shadow-raised">
              <step.icon className="size-4.5" />
            </span>
            <div className="min-w-0">
              <h3 className="font-medium">
                <span className="text-subtle-foreground tabular-nums">{index + 1}. </span>
                {step.title}
              </h3>
              <p className="mt-0.5 text-[13px] text-muted-foreground text-pretty">{step.text}</p>
            </div>
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        <Link to="/library" search={{ state: undefined, samples: undefined, open: undefined }} className={buttonVariants({ size: "lg" })}>
          <CloudUpload aria-hidden />
          Upload clips
        </Link>
        <Link to="/projects/new" className={buttonVariants({ variant: "secondary", size: "lg" })}>
          I already have clips
        </Link>
      </div>
    </section>
  );
}
