"use client"

import { Tooltip as TooltipPrimitive } from "@base-ui/react/tooltip"
import { cn } from "@/lib/utils"

function TooltipProvider({
  delay = 300,
  ...props
}: TooltipPrimitive.Provider.Props) {
  return (
    <TooltipPrimitive.Provider
      data-slot="tooltip-provider"
      delay={delay}
      {...props}
    />
  )
}

function Tooltip({ ...props }: TooltipPrimitive.Root.Props) {
  return <TooltipPrimitive.Root data-slot="tooltip" {...props} />
}

function TooltipTrigger({ ...props }: TooltipPrimitive.Trigger.Props) {
  return <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props} />
}

function TooltipContent({
  className,
  side = "top",
  sideOffset = 8,
  align = "center",
  alignOffset = 0,
  children,
  ...props
}: TooltipPrimitive.Popup.Props &
  Pick<
    TooltipPrimitive.Positioner.Props,
    "align" | "alignOffset" | "side" | "sideOffset"
  >) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Positioner
        align={align}
        alignOffset={alignOffset}
        side={side}
        sideOffset={sideOffset}
        className="isolate z-50"
      >
        <TooltipPrimitive.Popup
          data-slot="tooltip-content"
          className={cn(
            "z-50 inline-flex w-fit max-w-80 origin-(--transform-origin) items-center gap-2 rounded-[8px] bg-tooltip px-2.5 py-1.5 text-[12px] leading-[1.4] font-medium text-tooltip-foreground shadow-[0_0_0_1px_rgba(255,255,255,0.06),inset_0_1px_0_rgba(255,255,255,0.08),0_6px_16px_-6px_rgba(0,0,0,0.45)] has-data-[slot=kbd]:pr-1.5",
            "**:data-[slot=kbd]:bg-white/8 **:data-[slot=kbd]:bg-none **:data-[slot=kbd]:text-white/75 **:data-[slot=kbd]:shadow-[inset_0_0_0_1px_rgba(255,255,255,0.1),inset_0_1px_0_rgba(255,255,255,0.06)]",
            "transition-[opacity,scale,translate] duration-(--dur-fast) ease-out data-ending-style:scale-[0.97] data-ending-style:opacity-0 data-instant:duration-0 data-starting-style:scale-[0.96] data-starting-style:opacity-0",
            "data-[side=bottom]:data-starting-style:-translate-y-0.5 data-[side=left]:data-starting-style:translate-x-0.5 data-[side=right]:data-starting-style:-translate-x-0.5 data-[side=top]:data-starting-style:translate-y-0.5",
            className
          )}
          {...props}
        >
          <TooltipPrimitive.Arrow className="size-2 data-[side=bottom]:-top-1 data-[side=left]:-right-1 data-[side=right]:-left-1 data-[side=top]:-bottom-1">
            <span className="block size-full rotate-45 rounded-[1.5px] bg-tooltip" />
          </TooltipPrimitive.Arrow>
          {children}
        </TooltipPrimitive.Popup>
      </TooltipPrimitive.Positioner>
    </TooltipPrimitive.Portal>
  )
}

export { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider }
