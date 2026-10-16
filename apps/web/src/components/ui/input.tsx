import * as React from "react"
import { Input as InputPrimitive } from "@base-ui/react/input"
import { cn } from "@/lib/utils"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <InputPrimitive
      type={type}
      data-slot="input"
      className={cn(
        "h-8.5 w-full min-w-0 rounded-[10px] bg-well px-3 text-[13.5px] text-foreground shadow-well transition-[box-shadow,background-color] duration-(--dur-fast) outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-[13px] file:font-medium file:text-foreground placeholder:text-subtle-foreground hover:shadow-[inset_0_0_0_1px_var(--input),inset_0_1px_2px_rgba(11,11,11,0.05)]",
        "focus-visible:bg-surface-raised focus-visible:shadow-[inset_0_0_0_1px_var(--brand),0_0_0_3px_var(--brand-wash)] focus-visible:ring-0 focus-visible:ring-offset-0",
        "disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
        "aria-invalid:shadow-[inset_0_0_0_1px_var(--destructive),0_0_0_3px_color-mix(in_oklab,var(--destructive)_14%,transparent)]",
        className
      )}
      {...props}
    />
  )
}

export { Input }
