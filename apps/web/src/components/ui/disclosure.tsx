import { ChevronRight } from "lucide-react";
import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Something most people never need, one click away: a native `<details>`, closed unless `defaultOpen`. The technical side of the
 * app (ids, codes, costs, the editor's step list) lives in these, out of the way.
 */
export function Disclosure({
  summary,
  hint,
  defaultOpen = false,
  onToggle,
  className,
  bodyClassName,
  children,
  ...props
}: {
  summary: React.ReactNode;
  /** A quieter line after the summary: what is inside. */
  hint?: React.ReactNode;
  defaultOpen?: boolean;
  onToggle?: (open: boolean) => void;
  className?: string;
  bodyClassName?: string;
  children: React.ReactNode;
} & Omit<React.ComponentProps<"details">, "onToggle" | "open" | "children" | "className">) {
  return (
    <details {...props} open={defaultOpen || undefined} onToggle={(event) => onToggle?.(event.currentTarget.open)} className={cn("group/disclosure", className)}>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-md py-1 text-[13.5px] text-muted-foreground outline-none select-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-brand [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-4 shrink-0 transition-transform duration-(--dur-fast) group-open/disclosure:rotate-90" />
        <span className="font-medium">{summary}</span>
        {hint ? <span className="truncate text-subtle-foreground">{hint}</span> : null}
      </summary>
      <div className={cn("pt-2", bodyClassName)}>{children}</div>
    </details>
  );
}
