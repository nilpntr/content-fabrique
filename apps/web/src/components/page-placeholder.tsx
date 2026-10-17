import type * as React from "react";
import { Skeleton } from "@/components/ui/skeleton";

const CARD = "animate-rise rounded-xl bg-surface-raised p-4 shadow-soft ring-1 ring-border";

export function PagePlaceholder({
  title,
  description,
  cards = 3,
  children,
}: {
  title: string;
  description: string;
  cards?: number;
  children?: React.ReactNode;
}) {
  return (
    <div className="space-y-6">
      <div className="animate-rise">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">{title}</h1>
        <p className="mt-1.5 max-w-prose text-muted-foreground">{description}</p>
      </div>
      <div className="grid gap-4 md:grid-cols-3">
        {Array.from({ length: cards }, (_, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static skeleton cards, the list never reorders.
          <div key={i} className={`${CARD} space-y-3`} style={{ animationDelay: `${50 + i * 45}ms` }}>
            <Skeleton className="h-4 w-1/3" />
            <Skeleton className="h-8 w-1/2" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        ))}
      </div>
      {children ?? (
        <div className={`${CARD} space-y-3`} style={{ animationDelay: `${50 + cards * 45}ms` }}>
          <Skeleton className="h-4 w-1/4" />
          <Skeleton className="h-48 w-full" />
        </div>
      )}
    </div>
  );
}

export { CARD as cardClassName };
