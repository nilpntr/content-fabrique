import type * as React from "react";
import { cn } from "@/lib/utils";

/** The raised card every page uses for a block of content. */
export const cardClassName = "animate-rise rounded-xl bg-surface-raised p-4 shadow-soft ring-1 ring-border";

/** A page's title, a line under it and, on the right (below on a phone), its main action. */
export function PageHeader({
  title,
  description,
  actions,
  className,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap items-start justify-between gap-3", className)}>
      <div className="min-w-0 animate-rise">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">{title}</h1>
        {description ? <p className="mt-1.5 max-w-prose text-muted-foreground text-pretty">{description}</p> : null}
      </div>
      {actions ? <div className="flex animate-rise flex-wrap gap-2">{actions}</div> : null}
    </div>
  );
}
