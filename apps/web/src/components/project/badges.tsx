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
