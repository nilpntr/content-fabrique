import type * as React from "react";
import { formatInfo } from "@/components/new-project/formats";
import type { Format } from "@/components/new-project/types";
import type { ProjectStatus } from "@/lib/api/client";
import { cn } from "@/lib/utils";

const STATUS: Readonly<Record<ProjectStatus, { label: string; tone: string }>> = {
  draft: { label: "Draft", tone: "bg-wash text-muted-foreground" },
  editing: { label: "Editing", tone: "bg-brand-wash text-brand" },
  review: { label: "In review", tone: "bg-add-wash text-good" },
  approved: { label: "Approved", tone: "bg-add-wash text-good" },
  exported: { label: "Exported", tone: "bg-wash text-foreground" },
};

const PILL = "inline-flex h-5 items-center rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap";

const PILL_TONES = {
  neutral: "bg-wash text-muted-foreground",
  strong: "bg-wash text-foreground",
  brand: "bg-brand-wash text-brand",
  good: "bg-add-wash text-foreground",
  bad: "bg-del-wash text-destructive",
} as const;

export type PillTone = keyof typeof PILL_TONES;

/** A small rounded label: a content type, a tempo, a check result. `neutral` is quiet, `strong` plain text on the wash, `brand` the accent; `good` and `bad` are results. */
export function Pill({ tone = "neutral", className, children, ...props }: { tone?: PillTone } & React.ComponentProps<"span">) {
  return (
    <span {...props} className={cn(PILL, "gap-1", PILL_TONES[tone], className)}>
      {children}
    </span>
  );
}

/** The words for a project's status ("In review"); an unknown status reads as it is. */
export const statusLabel = (status: string): string => STATUS[status as ProjectStatus]?.label ?? status;

/** The state of a project: draft, editing, in review... An unknown status shows as it is. */
export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const known = STATUS[status as ProjectStatus];
  return <span className={cn(PILL, known?.tone ?? "bg-wash text-muted-foreground", className)}>{known?.label ?? status}</span>;
}

/** The output format: "9:16" with its name as a tooltip and for screen readers. */
export function FormatBadge({ format, className }: { format: string; className?: string }) {
  let name = "";
  try {
    name = formatInfo(format as Format).name;
  } catch {
    // A format this app does not know: show it as it is.
  }
  return (
    <span title={name || undefined} className={cn(PILL, "bg-wash font-mono text-foreground tabular-nums", className)}>
      {format}
      {name ? <span className="sr-only"> {name}</span> : null}
    </span>
  );
}
