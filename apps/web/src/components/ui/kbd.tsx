import type * as React from "react";
import { cn } from "@/lib/utils";

export function Kbd({ className, ...props }: React.ComponentProps<"kbd">) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-[5px] bg-linear-to-b from-surface-raised to-surface px-1 font-sans text-[11px] leading-none font-medium text-muted-foreground shadow-key select-none",
        className,
      )}
      {...props}
    />
  );
}
