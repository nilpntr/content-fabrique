"use client"

import * as React from "react"
import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"

import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Kbd } from "@/components/ui/kbd"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useHotkey, useModKey } from "@/hooks/use-hotkey"
import { Glider, handOff } from "@/lib/glide"
import { sidebarCookie } from "@/lib/sidebar-state"

const SIDEBAR_WIDTH = "16rem"
const SIDEBAR_WIDTH_MOBILE = "18rem"
const SIDEBAR_WIDTH_ICON = "3rem"
const SIDEBAR_KEYBOARD_SHORTCUT = "b"

type SidebarContextProps = {
  state: "expanded" | "collapsed"
  open: boolean
  setOpen: (open: boolean) => void
  openMobile: boolean
  setOpenMobile: (open: boolean) => void
  isMobile: boolean
  toggleSidebar: () => void
}

const SidebarContext = React.createContext<SidebarContextProps | null>(null)

function useSidebar() {
  const context = React.useContext(SidebarContext)
  if (!context) {
    throw new Error("useSidebar must be used within a SidebarProvider.")
  }

  return context
}

function SidebarProvider({
  defaultOpen = true,
  open: openProp,
  onOpenChange: setOpenProp,
  className,
  style,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const isMobile = useIsMobile()
  const [openMobile, setOpenMobile] = React.useState(false)

  const [_open, _setOpen] = React.useState(defaultOpen)
  const open = openProp ?? _open
  const setOpen = React.useCallback(
    (value: boolean | ((value: boolean) => boolean)) => {
      const openState = typeof value === "function" ? value(open) : value
      if (setOpenProp) {
        setOpenProp(openState)
      } else {
        _setOpen(openState)
      }

      // biome-ignore lint/suspicious/noDocumentCookie: shadcn persists the sidebar state in a cookie that the server can read; the Cookie Store API is not available everywhere yet.
      document.cookie = sidebarCookie(openState)
    },
    [setOpenProp, open]
  )

  const toggleSidebar = React.useCallback(() => {
    return isMobile ? setOpenMobile((open) => !open) : setOpen((open) => !open)
  }, [isMobile, setOpen])

  // Not while typing: Ctrl+B is "cursor back" in text fields on macOS.
  useHotkey(SIDEBAR_KEYBOARD_SHORTCUT, toggleSidebar, {
    mod: true,
    allowWhileTyping: false,
  })

  const state = open ? "expanded" : "collapsed"

  const contextValue = React.useMemo<SidebarContextProps>(
    () => ({
      state,
      open,
      setOpen,
      isMobile,
      openMobile,
      setOpenMobile,
      toggleSidebar,
    }),
    [state, open, setOpen, isMobile, openMobile, toggleSidebar]
  )

  return (
    <SidebarContext.Provider value={contextValue}>
      <div
        data-slot="sidebar-wrapper"
        style={
          {
            "--sidebar-width": SIDEBAR_WIDTH,
            "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
            ...style,
          } as React.CSSProperties
        }
        className={cn(
          "group/sidebar-wrapper flex min-h-svh w-full has-data-[variant=inset]:bg-sidebar",
          className
        )}
        {...props}
      >
        {children}
      </div>
    </SidebarContext.Provider>
  )
}

function Sidebar({
  side = "left",
  variant = "sidebar",
  collapsible = "offcanvas",
  className,
  children,
  dir,
  ...props
}: React.ComponentProps<"div"> & {
  side?: "left" | "right"
  variant?: "sidebar" | "floating" | "inset"
  collapsible?: "offcanvas" | "icon" | "none"
}) {
  const { isMobile, state, openMobile, setOpenMobile } = useSidebar()

  if (collapsible === "none") {
    return (
      <div
        data-slot="sidebar"
        className={cn(
          "flex h-full w-(--sidebar-width) flex-col bg-sidebar text-sidebar-foreground",
          className
        )}
        {...props}
      >
        {children}
      </div>
    )
  }

  if (isMobile) {
    return (
      <Sheet open={openMobile} onOpenChange={setOpenMobile} {...props}>
        <SheetContent
          dir={dir}
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          className="w-(--sidebar-width) bg-sidebar p-0 text-sidebar-foreground ring-0 [&>button]:hidden"
          style={
            {
              "--sidebar-width": SIDEBAR_WIDTH_MOBILE,
            } as React.CSSProperties
          }
          side={side}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Displays the mobile sidebar.</SheetDescription>
          </SheetHeader>
          <div className="flex h-full w-full flex-col p-2">{children}</div>
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <div
      className="group peer hidden text-sidebar-foreground md:block"
      data-state={state}
      data-collapsible={state === "collapsed" ? collapsible : ""}
      data-variant={variant}
      data-side={side}
      data-slot="sidebar"
    >
      <div
        data-slot="sidebar-gap"
        className={cn(
          "relative w-(--sidebar-width) bg-transparent transition-[width] duration-(--dur-slow) ease-out",
          "group-data-[collapsible=offcanvas]:w-0",
          "group-data-[side=right]:rotate-180",
          variant === "inset"
            ? "w-[calc(var(--sidebar-width)+(--spacing(2)))] group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(6)))]"
            : variant === "floating"
              ? "group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4)))]"
              : "group-data-[collapsible=icon]:w-(--sidebar-width-icon)"
        )}
      />
      <div
        data-slot="sidebar-container"
        data-side={side}
        className={cn(
          "fixed inset-y-0 z-10 hidden h-svh w-(--sidebar-width) transition-[left,right,width] duration-(--dur-slow) ease-out data-[side=left]:left-0 data-[side=left]:group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)] data-[side=right]:right-0 data-[side=right]:group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)] md:flex",
          variant === "inset"
            ? "p-2 group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4)))]"
            : variant === "floating"
              ? "p-2 group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4))+2px)]"
              : "group-data-[collapsible=icon]:w-(--sidebar-width-icon) group-data-[side=left]:border-r group-data-[side=right]:border-l",
          className
        )}
        {...props}
      >
        <div
          data-sidebar="sidebar"
          data-slot="sidebar-inner"
          className="flex size-full flex-col overflow-hidden bg-sidebar group-data-[variant=floating]:rounded-lg group-data-[variant=floating]:shadow-sm group-data-[variant=floating]:ring-1 group-data-[variant=floating]:ring-sidebar-border"
        >
          {children}
        </div>
      </div>
    </div>
  )
}

function PanelGlyph() {
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden className="size-4">
      <rect x="1.7" y="2.45" width="12.6" height="11.1" rx="3.2" stroke="currentColor" strokeWidth="1.4" />
      <rect
        x="3.75"
        y="4.5"
        width="4.6"
        height="7"
        rx="1.2"
        fill="currentColor"
        className="origin-left scale-x-35 opacity-60 transition-[scale,opacity] duration-(--dur-spring) ease-spring [transform-box:fill-box] group-hover/trigger:scale-x-75 group-hover/trigger:opacity-90 group-data-open/trigger:scale-x-100 group-data-open/trigger:opacity-90 group-data-open/trigger:group-hover/trigger:scale-x-50"
      />
    </svg>
  )
}

function SidebarTrigger({
  className,
  onClick,
  ...props
}: React.ComponentProps<typeof Button>) {
  const { toggleSidebar, state, isMobile, openMobile } = useSidebar()
  const open = isMobile ? openMobile : state === "expanded"
  const modKey = useModKey()

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            data-sidebar="trigger"
            data-slot="sidebar-trigger"
            data-open={open ? "" : undefined}
            variant="ghost"
            size="icon-sm"
            className={cn("group/trigger", className)}
            onClick={(event) => {
              onClick?.(event)
              toggleSidebar()
            }}
            {...props}
          />
        }
      >
        <PanelGlyph />
        <span className="sr-only">Toggle Sidebar</span>
      </TooltipTrigger>
      <TooltipContent side="bottom" align="start" hidden={isMobile}>
        {open ? "Collapse sidebar" : "Expand sidebar"}
        <Kbd>{modKey}</Kbd>
        <Kbd className="-ml-1">B</Kbd>
      </TooltipContent>
    </Tooltip>
  )
}

function SidebarRail({ className, onPointerEnter, onPointerMove, ...props }: React.ComponentProps<"button">) {
  const { toggleSidebar } = useSidebar()
  const handle = React.useRef<HTMLSpanElement>(null)
  const follow = (event: React.PointerEvent<HTMLButtonElement>, snap: boolean) => {
    const el = handle.current
    if (!el) return
    const y = event.clientY - event.currentTarget.getBoundingClientRect().top - el.offsetHeight / 2
    if (snap) el.style.transitionProperty = "opacity, scale"
    el.style.translate = `-50% ${Math.round(y)}px`
    if (snap) {
      void getComputedStyle(el).translate
      el.style.transitionProperty = ""
    }
  }

  return (
    <button
      data-sidebar="rail"
      data-slot="sidebar-rail"
      aria-label="Toggle Sidebar"
      tabIndex={-1}
      onClick={toggleSidebar}
      onPointerEnter={(event) => {
        onPointerEnter?.(event)
        follow(event, true)
      }}
      onPointerMove={(event) => {
        onPointerMove?.(event)
        follow(event, false)
      }}
      className={cn(
        "group/rail absolute inset-y-0 z-20 hidden w-4 group-data-[side=left]:-right-4 group-data-[side=right]:left-0 md:flex ltr:-translate-x-1/2 rtl:-translate-x-1/2",
        "in-data-[side=left]:cursor-w-resize in-data-[side=right]:cursor-e-resize",
        "[[data-side=left][data-state=collapsed]_&]:cursor-e-resize [[data-side=right][data-state=collapsed]_&]:cursor-w-resize",
        "group-data-[collapsible=offcanvas]:translate-x-0 hover:group-data-[collapsible=offcanvas]:bg-sidebar",
        "[[data-side=left][data-collapsible=offcanvas]_&]:-right-2",
        "[[data-side=right][data-collapsible=offcanvas]_&]:-left-2",
        className
      )}
      {...props}
    >
      <span
        ref={handle}
        aria-hidden
        className="pointer-events-none absolute top-0 left-1/2 z-10 h-10 w-1.5 -translate-x-1/2 scale-y-50 rounded-full bg-surface-raised opacity-0 shadow-raised transition-[translate,scale,opacity] duration-(--dur-base) group-hover/rail:scale-y-100 group-hover/rail:opacity-100 group-active/rail:scale-x-75"
      />
    </button>
  )
}

function SidebarInset({ className, ...props }: React.ComponentProps<"main">) {
  return (
    <main
      data-slot="sidebar-inset"
      className={cn(
        "relative flex w-full flex-1 flex-col bg-background md:peer-data-[variant=inset]:m-2 md:peer-data-[variant=inset]:ml-0 md:peer-data-[variant=inset]:rounded-xl md:peer-data-[variant=inset]:shadow-sm",
        className
      )}
      {...props}
    />
  )
}

function SidebarInput({
  className,
  ...props
}: React.ComponentProps<typeof Input>) {
  return (
    <Input
      data-slot="sidebar-input"
      data-sidebar="input"
      className={cn("h-8 w-full bg-background shadow-none", className)}
      {...props}
    />
  )
}

function SidebarHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-header"
      data-sidebar="header"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  )
}

function SidebarFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-footer"
      data-sidebar="footer"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  )
}

function SidebarSeparator({
  className,
  ...props
}: React.ComponentProps<typeof Separator>) {
  return (
    <Separator
      data-slot="sidebar-separator"
      data-sidebar="separator"
      className={cn("mx-2 w-auto bg-transparent bg-linear-to-r from-transparent via-sidebar-border to-transparent", className)}
      {...props}
    />
  )
}

type SidebarScope = { lens: HTMLElement | null }

const SidebarScopeContext = React.createContext<SidebarScope | null>(null)

function menuButtonAt(target: EventTarget | null): HTMLElement | null {
  const button =
    target instanceof Element
      ? target.closest<HTMLElement>('[data-sidebar="menu-button"], [data-sidebar="menu-sub-button"]')
      : null
  return button && !button.matches(":disabled, [aria-disabled='true']") ? button : null
}

function SidebarContent({
  className,
  children,
  onPointerOver,
  onPointerLeave,
  onFocus,
  onBlur,
  ...props
}: React.ComponentProps<"div">) {
  const { state } = useSidebar()
  const [scope] = React.useState<SidebarScope>(() => ({ lens: null }))
  const ref = React.useRef<HTMLDivElement>(null)
  const hoverRef = React.useRef<HTMLSpanElement>(null)
  const focusRef = React.useRef<HTMLSpanElement>(null)
  const gliders = React.useRef<{ hover: Glider; focus: Glider } | null>(null)

  React.useLayoutEffect(() => {
    if (!ref.current || !hoverRef.current || !focusRef.current) return
    const current = { hover: new Glider(hoverRef.current, ref.current), focus: new Glider(focusRef.current, ref.current) }
    gliders.current = current
    return () => {
      current.hover.destroy()
      current.focus.destroy()
      gliders.current = null
    }
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: `state` is the trigger, so the hover highlight resets when the sidebar expands or collapses.
  React.useEffect(() => {
    gliders.current?.hover.to(null)
  }, [state])

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the handlers only move a decorative hover and focus highlight; the menu buttons inside stay the interactive elements.
    <div
      ref={ref}
      data-slot="sidebar-content"
      data-sidebar="content"
      className={cn(
        "no-scrollbar relative isolate flex min-h-0 flex-1 flex-col gap-0 overflow-auto group-data-[collapsible=icon]:overflow-hidden",
        className
      )}
      onPointerOver={(event) => {
        onPointerOver?.(event)
        if (event.pointerType === "touch") return
        const button = menuButtonAt(event.target)
        gliders.current?.hover.to(button, button ? 0 : 120)
      }}
      onPointerLeave={(event) => {
        onPointerLeave?.(event)
        gliders.current?.hover.to(null)
      }}
      onFocus={(event) => {
        onFocus?.(event)
        const button = menuButtonAt(event.target)
        gliders.current?.focus.to(button?.matches(":focus-visible") ? button : null)
      }}
      onBlur={(event) => {
        onBlur?.(event)
        if (!(event.relatedTarget instanceof Node && ref.current?.contains(event.relatedTarget))) {
          gliders.current?.focus.to(null)
        }
      }}
      {...props}
    >
      <span ref={hoverRef} aria-hidden data-slot="sidebar-hover" className="glider -z-1 rounded-[10px] bg-sidebar-accent" />
      <SidebarScopeContext.Provider value={scope}>{children}</SidebarScopeContext.Provider>
      <span
        ref={focusRef}
        aria-hidden
        data-slot="sidebar-focus"
        className="glider z-1 rounded-[10px] ring-2 ring-brand/80 ring-inset dark:ring-brand/70"
      />
    </div>
  )
}

function SidebarGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group"
      data-sidebar="group"
      className={cn("relative flex w-full min-w-0 flex-col p-2", className)}
      {...props}
    />
  )
}

function SidebarGroupLabel({
  className,
  render,
  ...props
}: useRender.ComponentProps<"div"> & React.ComponentProps<"div">) {
  return useRender({
    defaultTagName: "div",
    props: mergeProps<"div">(
      {
        className: cn(
          "flex h-7 shrink-0 items-center gap-2 rounded-md px-2 text-[10.5px] font-medium tracking-[0.08em] whitespace-nowrap text-subtle-foreground uppercase ring-sidebar-ring outline-hidden [transition:margin_var(--dur-slow)_var(--motion-out),opacity_var(--dur-base)_var(--motion-out)_90ms] group-data-[collapsible=icon]:-mt-7 group-data-[collapsible=icon]:opacity-0 group-data-[collapsible=icon]:[transition:margin_var(--dur-slow)_var(--motion-out),opacity_var(--dur-fast)_var(--motion-out)] focus-visible:ring-2 [&>svg]:size-4 [&>svg]:shrink-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-group-label",
      sidebar: "group-label",
    },
  })
}

function SidebarGroupAction({
  className,
  render,
  ...props
}: useRender.ComponentProps<"button"> & React.ComponentProps<"button">) {
  return useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          "absolute top-3.5 right-3 flex aspect-square w-5 cursor-pointer items-center justify-center rounded-[6px] p-0 text-subtle-foreground ring-sidebar-ring outline-hidden transition-colors group-data-[collapsible=icon]:hidden after:absolute after:-inset-2 hover:bg-sidebar-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-offset-sidebar md:after:hidden [&>svg]:size-4 [&>svg]:shrink-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-group-action",
      sidebar: "group-action",
    },
  })
}

function SidebarGroupContent({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group-content"
      data-sidebar="group-content"
      className={cn("w-full text-sm", className)}
      {...props}
    />
  )
}

function SidebarMenu({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu"
      data-sidebar="menu"
      className={cn("flex w-full min-w-0 flex-col gap-0.5", className)}
      {...props}
    />
  )
}

function SidebarMenuItem({ className, ...props }: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-item"
      data-sidebar="menu-item"
      className={cn("group/menu-item relative", className)}
      {...props}
    />
  )
}

const sidebarMenuButtonVariants = cva(
  "sidebar-item peer/menu-button group/menu-button relative flex w-full cursor-pointer items-center gap-2.5 rounded-[10px] px-2 text-left text-[13px] text-muted-foreground ring-sidebar-ring outline-hidden select-none group-has-data-[sidebar=menu-action]/menu-item:pr-8 group-data-[collapsible=icon]:size-8! hover:text-foreground focus-visible:ring-offset-sidebar disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-popup-open:text-foreground data-active:text-foreground [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "",
        outline: "bg-surface-raised shadow-raised hover:bg-surface",
      },
      size: {
        default: "h-9",
        sm: "h-7 text-xs",
        lg: "h-12",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function SidebarMenuLabel({
  children,
  className,
  ...props
}: Omit<React.ComponentProps<"span">, "children"> & { children: string }) {
  return (
    <span data-slot="sidebar-menu-label" data-label={children} className={cn("sidebar-label", className)} {...props}>
      <span>{children}</span>
    </span>
  )
}

function SidebarMenuButton({
  render,
  isActive = false,
  variant = "default",
  size = "default",
  tooltip,
  className,
  children,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    isActive?: boolean
    tooltip?: string | React.ComponentProps<typeof TooltipContent>
  } & VariantProps<typeof sidebarMenuButtonVariants>) {
  const { isMobile, state } = useSidebar()
  const scope = React.useContext(SidebarScopeContext)
  const lensRef = React.useRef<HTMLSpanElement>(null)

  React.useLayoutEffect(() => {
    const lens = lensRef.current
    if (!scope || !lens) return
    if (!isActive) return
    // `scope.lens` stays on the last active item, also while no item is active (a page outside the nav),
    // so the lens still glides from where it was.
    const prev = scope.lens
    scope.lens = lens
    if (prev && prev !== lens && prev.isConnected) {
      handOff(prev, lens, lens.parentElement?.querySelector(":scope > svg") ?? null)
    }
  }, [scope, isActive])

  // Only when the item unmounts: deactivating must keep `scope.lens`, it is the glide's starting point.
  React.useLayoutEffect(() => {
    const lens = lensRef.current
    return () => {
      if (scope && scope.lens === lens) scope.lens = null
    }
  }, [scope])

  const comp = useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          sidebarMenuButtonVariants({ variant, size }),
          scope
            ? "focus-visible:ring-0 focus-visible:ring-offset-0"
            : "hover:bg-sidebar-accent focus-visible:ring-2 data-popup-open:bg-sidebar-accent",
          className
        ),
        children: (
          <>
            {scope && <span ref={lensRef} aria-hidden data-slot="sidebar-menu-lens" className="sidebar-lens" />}
            {children}
          </>
        ),
      },
      props
    ),
    render: !tooltip ? render : <TooltipTrigger render={render} />,
    state: {
      slot: "sidebar-menu-button",
      sidebar: "menu-button",
      size,
      active: isActive,
    },
  })

  if (!tooltip) {
    return comp
  }

  if (typeof tooltip === "string") {
    tooltip = {
      children: tooltip,
    }
  }

  return (
    <Tooltip>
      {comp}
      <TooltipContent
        side="right"
        align="center"
        hidden={state !== "collapsed" || isMobile}
        {...tooltip}
      />
    </Tooltip>
  )
}

function SidebarMenuAction({
  className,
  render,
  showOnHover = false,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    showOnHover?: boolean
  }) {
  return useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          "absolute top-2 right-1.5 flex aspect-square w-5 cursor-pointer items-center justify-center rounded-[6px] p-0 text-subtle-foreground ring-sidebar-ring outline-hidden transition-colors group-data-[collapsible=icon]:hidden peer-hover/menu-button:text-muted-foreground peer-data-[size=default]/menu-button:top-2 peer-data-[size=lg]/menu-button:top-3.5 peer-data-[size=sm]/menu-button:top-1 after:absolute after:-inset-2 hover:bg-sidebar-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-offset-sidebar md:after:hidden [&>svg]:size-4 [&>svg]:shrink-0",
          showOnHover &&
            "transition-[opacity,color] group-focus-within/menu-item:opacity-100 group-hover/menu-item:opacity-100 peer-data-active/menu-button:text-muted-foreground aria-expanded:opacity-100 md:opacity-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-action",
      sidebar: "menu-action",
    },
  })
}

function SidebarMenuBadge({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-menu-badge"
      data-sidebar="menu-badge"
      className={cn(
        "pointer-events-none absolute right-2 flex h-5 min-w-5 items-center justify-center rounded-full bg-wash px-1.5 text-[11px] font-medium text-muted-foreground tabular-nums shadow-[inset_0_0_0_1px_var(--border)] transition-colors select-none group-data-[collapsible=icon]:hidden peer-hover/menu-button:text-foreground peer-data-[size=default]/menu-button:top-2 peer-data-[size=lg]/menu-button:top-3.5 peer-data-[size=sm]/menu-button:top-1 peer-data-active/menu-button:bg-brand-wash peer-data-active/menu-button:text-brand peer-data-active/menu-button:shadow-none",
        className
      )}
      {...props}
    />
  )
}

function SidebarMenuSkeleton({
  className,
  showIcon = false,
  ...props
}: React.ComponentProps<"div"> & {
  showIcon?: boolean
}) {
  const [width] = React.useState(() => {
    return `${Math.floor(Math.random() * 40) + 50}%`
  })

  return (
    <div
      data-slot="sidebar-menu-skeleton"
      data-sidebar="menu-skeleton"
      className={cn("flex h-9 items-center gap-2.5 rounded-[10px] px-2", className)}
      {...props}
    >
      {showIcon && (
        <Skeleton
          className="size-4 rounded-md"
          data-sidebar="menu-skeleton-icon"
        />
      )}
      <Skeleton
        className="h-4 max-w-(--skeleton-width) flex-1"
        data-sidebar="menu-skeleton-text"
        style={
          {
            "--skeleton-width": width,
          } as React.CSSProperties
        }
      />
    </div>
  )
}

function SidebarMenuSub({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu-sub"
      data-sidebar="menu-sub"
      className={cn(
        "ml-4 flex min-w-0 flex-col gap-0.5 border-l border-sidebar-border py-0.5 pl-2.5 group-data-[collapsible=icon]:hidden",
        className
      )}
      {...props}
    />
  )
}

function SidebarMenuSubItem({
  className,
  ...props
}: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-sub-item"
      data-sidebar="menu-sub-item"
      className={cn("group/menu-sub-item relative", className)}
      {...props}
    />
  )
}

function SidebarMenuSubButton({
  render,
  size = "md",
  isActive = false,
  className,
  ...props
}: useRender.ComponentProps<"a"> &
  React.ComponentProps<"a"> & {
    size?: "sm" | "md"
    isActive?: boolean
  }) {
  return useRender({
    defaultTagName: "a",
    props: mergeProps<"a">(
      {
        className: cn(
          "relative flex h-7 min-w-0 cursor-pointer items-center gap-2 rounded-[8px] px-2 text-muted-foreground outline-hidden transition-colors group-data-[collapsible=icon]:hidden hover:text-foreground focus-visible:ring-0 focus-visible:ring-offset-0 disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-[size=md]:text-[13px] data-[size=sm]:text-xs data-active:font-medium data-active:text-foreground [&>span:last-child]:truncate [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-subtle-foreground",
          "before:absolute before:inset-y-1.5 before:-left-[11px] before:w-[1.5px] before:rounded-full before:bg-brand before:opacity-0 before:shadow-[0_0_6px_var(--lens-glow)] before:transition-opacity data-active:before:opacity-100",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-sub-button",
      sidebar: "menu-sub-button",
      size,
      active: isActive,
    },
  })
}

export {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInput,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuLabel,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
  SidebarTrigger,
  useSidebar,
}
