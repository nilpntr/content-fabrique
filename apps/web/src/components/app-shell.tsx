import { Outlet, useRouterState } from "@tanstack/react-router";
import { SearchIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { AppSidebar } from "@/components/app-sidebar";
import { CommandPalette } from "@/components/command-palette";
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage } from "@/components/ui/breadcrumb";
import { Kbd } from "@/components/ui/kbd";
import { SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { useHotkey, useModKey } from "@/hooks/use-hotkey";
import { MAIN_SCROLL_ID } from "@/lib/scroll";
import { useActiveNavItem } from "@/nav";
import { cn } from "@/lib/utils";

function useScrolled(threshold = 4) {
  const ref = useRef<HTMLElement>(null);
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onScroll = () => setScrolled(el.scrollTop > threshold);
    onScroll();
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [threshold]);
  return { ref, scrolled };
}

export function AppShell({ defaultSidebarOpen = true }: { defaultSidebarOpen?: boolean }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const activeItem = useActiveNavItem();
  const title = activeItem?.title ?? "Not found";
  const modKey = useModKey();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const { ref, scrolled } = useScrolled();
  useHotkey("k", () => setPaletteOpen((open) => !open), { mod: true });

  return (
    <SidebarProvider defaultOpen={defaultSidebarOpen}>
      <AppSidebar active={activeItem} />
      <SidebarInset
        ref={ref}
        data-scroll-restoration-id={MAIN_SCROLL_ID}
        className="isolate h-svh overflow-y-auto bg-background shadow-soft ring-1 ring-border md:h-[calc(100svh-1rem)] md:peer-data-[variant=inset]:rounded-xl md:peer-data-[variant=inset]:shadow-soft"
      >
        <div aria-hidden className="page-glow pointer-events-none absolute inset-x-0 top-0 -z-10 h-[24rem]" />
        <header
          className={cn(
            "sticky top-0 z-20 flex h-14 shrink-0 items-center gap-2.5 border-b px-[18px] transition-[background-color,border-color] duration-(--dur-slow)",
            scrolled ? "border-hairline bg-background/80 backdrop-blur-xl backdrop-saturate-150" : "border-transparent",
          )}
        >
          <SidebarTrigger />
          <Breadcrumb className="min-w-0">
            <BreadcrumbList>
              <BreadcrumbItem>
                <BreadcrumbPage key={pathname} className="animate-rise truncate">
                  {title}
                </BreadcrumbPage>
              </BreadcrumbItem>
            </BreadcrumbList>
          </Breadcrumb>
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            aria-label="Search"
            aria-keyshortcuts={modKey === "⌘" ? "Meta+K" : "Control+K"}
            className="group ml-auto flex h-8 w-8 shrink-0 cursor-pointer items-center gap-2 rounded-[10px] bg-well px-2 text-[13px] text-subtle-foreground shadow-well transition-[color,box-shadow] hover:text-muted-foreground hover:shadow-[inset_0_0_0_1px_var(--input),inset_0_1px_2px_rgba(11,11,11,0.05)] sm:w-64 sm:px-2.5"
          >
            <SearchIcon className="size-4 shrink-0" />
            <span className="hidden flex-1 truncate text-left sm:inline">Search or jump to…</span>
            <span className="hidden items-center gap-0.5 sm:inline-flex">
              <Kbd>{modKey}</Kbd>
              <Kbd>K</Kbd>
            </span>
          </button>
        </header>
        <div className="flex-1 px-6 pt-6 pb-12">
          <Outlet />
        </div>
      </SidebarInset>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </SidebarProvider>
  );
}
