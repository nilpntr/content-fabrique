import type * as React from "react";
import { cn } from "@/lib/utils";

/** A small toggle pill. Pass `pressed` for a toggle (tag filters); leave it out for a plain action. */
export function Chip({
  pressed,
  className,
  ...props
}: Omit<React.ComponentProps<"button">, "type"> & { pressed?: boolean }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      data-slot="chip"
      className={cn(
        "inline-flex h-7 cursor-pointer items-center gap-1 rounded-full bg-surface-raised px-2.5 text-[13px] font-medium text-muted-foreground shadow-raised select-none",
        "transition-[color,background-color,box-shadow] duration-(--dur-fast) ease-out hover:text-foreground",
        "aria-pressed:bg-brand-wash aria-pressed:text-brand aria-pressed:shadow-[inset_0_0_0_1px_var(--brand)]",
        className,
      )}
      {...props}
    />
  );
}
