import type * as React from "react";
import { cn } from "@/lib/utils";

/** The text-field look of `Input`, for several lines. */
function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "min-h-28 w-full min-w-0 resize-y rounded-[10px] bg-well px-3 py-2.5 text-[13.5px] leading-6 text-foreground shadow-well transition-[box-shadow,background-color] duration-(--dur-fast) outline-none placeholder:text-subtle-foreground hover:shadow-[inset_0_0_0_1px_var(--input),inset_0_1px_2px_rgba(11,11,11,0.05)]",
        "focus-visible:bg-surface-raised focus-visible:shadow-[inset_0_0_0_1px_var(--brand),0_0_0_3px_var(--brand-wash)] focus-visible:ring-0 focus-visible:ring-offset-0",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "aria-invalid:shadow-[inset_0_0_0_1px_var(--destructive),0_0_0_3px_color-mix(in_oklab,var(--destructive)_14%,transparent)]",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
