import { Link } from "@tanstack/react-router";
import { Coins } from "lucide-react";
import { buttonVariants } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogTitle } from "@/components/ui/dialog";
import { useMe } from "@/lib/api/hooks";
import { type Credits, creditsLabel, creditsOf, dismissCreditsExhausted, formatDay, useCreditsExhausted } from "@/lib/credits";
import { cn } from "@/lib/utils";
import { CREDITS_PER_VIDEO, videosLeftText, videosShort } from "@/lib/words";

/** The share of the allowance that is left, as a ring: full when nothing is used. */
function Ring({ credits, className }: { credits: Credits; className?: string }) {
  const share = credits.allowance > 0 ? Math.min(credits.available / credits.allowance, 1) : credits.available > 0 ? 1 : 0;
  const radius = 6;
  const length = 2 * Math.PI * radius;
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={cn("size-4 -rotate-90", className)}>
      <circle cx="8" cy="8" r={radius} fill="none" stroke="currentColor" strokeOpacity="0.18" strokeWidth="2" />
      <circle cx="8" cy="8" r={radius} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeDasharray={`${length * share} ${length}`} className="transition-[stroke-dasharray] duration-(--dur-slow)" />
    </svg>
  );
}

/** What the balance says in a sentence, for the label and the tooltip: in videos first, the exact credits after. */
export function creditsSummary(credits: Credits): string {
  const parts = [`${videosLeftText(credits)} (${creditsLabel(credits.available)})`];
  if (credits.period_end && credits.allowance > 0) parts.push(`renews ${formatDay(credits.period_end)}`);
  return parts.join(", ");
}

/**
 * The balance in the header, counted in videos (a creator thinks in videos, not credits): a ring and the videos left, amber when
 * the allowance is nearly used, red at zero. It is a link to Settings, where the balance is spelled out. Nothing renders when the
 * API has no credits for the workspace.
 */
export function CreditsChip({ credits, className }: { credits: Credits | undefined; className?: string }) {
  if (!credits) return null;
  const empty = credits.available === 0;
  return (
    <Link
      to="/settings"
      data-testid="credits-chip"
      data-state={empty ? "empty" : credits.low ? "low" : "ok"}
      aria-label={creditsSummary(credits)}
      title={creditsSummary(credits)}
      className={cn(
        "flex h-8 shrink-0 items-center gap-2 rounded-[10px] bg-well px-2.5 text-[13px] font-medium tabular-nums shadow-well outline-none transition-[color,box-shadow] hover:shadow-[inset_0_0_0_1px_var(--input),inset_0_1px_2px_rgba(11,11,11,0.05)] focus-visible:ring-2 focus-visible:ring-brand",
        empty ? "text-destructive" : credits.low ? "text-foreground" : "text-muted-foreground hover:text-foreground",
        className,
      )}
    >
      <Ring credits={credits} className={empty ? "text-destructive" : credits.low ? "text-warning" : "text-brand"} />
      <span>{videosShort(credits)}</span>
    </Link>
  );
}

/** The chip of the signed-in workspace: the balance from `GET /v1/me`, once the API has one. */
export function WorkspaceCreditsChip({ className }: { className?: string }) {
  const me = useMe();
  return <CreditsChip credits={creditsOf(me.data)} className={className} />;
}

/**
 * Shown when the API refuses a call with 402 `credits_exhausted`: the editor needs credits and the workspace has too few.
 * It names the balance and the renewal date when it knows them. There is no checkout yet, so it does not offer one.
 */
export function CreditsExhaustedDialog() {
  const open = useCreditsExhausted();
  const me = useMe();
  const credits = creditsOf(me.data);
  return (
    <Dialog open={open} onOpenChange={(next) => !next && dismissCreditsExhausted()}>
      <DialogContent hideClose>
        <div className="mb-3 flex size-9 items-center justify-center rounded-xl bg-del-wash text-destructive">
          <Coins aria-hidden className="size-4.5" />
        </div>
        <DialogTitle>You've used up your videos for now</DialogTitle>
        <DialogDescription>
          Making a video or changing one uses credits, and there aren't enough left for this.
          {credits?.period_end && credits.allowance > 0 ? ` You get new ones on ${formatDay(credits.period_end)}.` : ""} Nothing was charged and your video is untouched.
        </DialogDescription>
        {credits ? (
          <p className="mt-3 rounded-lg bg-well px-3 py-2 text-[13px] tabular-nums shadow-well">
            <span className="font-medium">{videosLeftText(credits)}</span>
            <span className="text-muted-foreground"> · {creditsLabel(credits.available)} left</span>
          </p>
        ) : null}
        <DialogFooter>
          <DialogClose className={buttonVariants({ variant: "ghost", size: "md" })}>Close</DialogClose>
          <Link to="/settings" onClick={dismissCreditsExhausted} className={buttonVariants({ size: "md" })}>
            See your plan
          </Link>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** The Settings card: the balance spelled out, in videos first. Nothing renders when the API has no credits for the workspace. */
export function CreditsCard({ credits, className }: { credits: Credits | undefined; className?: string }) {
  if (!credits) return null;
  const used = credits.allowance > 0 ? Math.max(credits.allowance - credits.available, 0) : 0;
  const share = credits.allowance > 0 ? Math.min(Math.max(used / credits.allowance, 0), 1) : 0;
  return (
    <section aria-label="Credits" data-testid="credits-card" className={cn("space-y-3", className)}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <p className="text-[1.75rem] leading-none font-semibold tracking-[-0.03em] tabular-nums">{videosLeftText(credits)}</p>
        {credits.low && credits.available > 0 ? <span className="rounded-full bg-warning/15 px-2 py-0.5 text-[11.5px] font-medium">Running low</span> : null}
        {credits.available === 0 ? <span className="rounded-full bg-del-wash px-2 py-0.5 text-[11.5px] font-medium text-destructive">None left</span> : null}
      </div>
      {credits.allowance > 0 ? (
        <div className="space-y-1.5">
          <div role="img" aria-label={`${used} of ${credits.allowance} credits of the allowance used`} className="h-1.5 w-full max-w-sm overflow-hidden rounded-full bg-wash">
            <div className={cn("h-full rounded-full transition-[width] duration-(--dur-slow)", credits.low ? "bg-warning" : "bg-brand")} style={{ width: `${share * 100}%` }} />
          </div>
          <p className="text-[13px] text-muted-foreground">
            {creditsLabel(credits.available)} left of {credits.allowance} this month{credits.period_end ? `, renews ${formatDay(credits.period_end)}` : ""}.
          </p>
        </div>
      ) : (
        <p className="text-[13px] text-muted-foreground tabular-nums">{creditsLabel(credits.available)} left.</p>
      )}
      <p className="max-w-prose text-[13px] text-muted-foreground text-pretty">
        One video is {CREDITS_PER_VIDEO} credits: the first edit and two rounds of changes. Exporting is free.
      </p>
      {credits.top_ups.length > 0 ? (
        <ul aria-label="Top-ups" className="space-y-1 text-[13px]">
          {credits.top_ups.map((topUp) => (
            <li key={`${topUp.expires_at}-${topUp.credits}`} className="flex gap-2">
              <span className="font-medium tabular-nums">{creditsLabel(topUp.credits)}</span>
              <span className="text-muted-foreground">extra, valid until {formatDay(topUp.expires_at)}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}
