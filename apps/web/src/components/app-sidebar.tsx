import { Link, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { APP_NAME, Wordmark } from "@/components/logo";
import { NavUser } from "@/components/nav-user";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuLabel,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar";
import { NAV_ITEMS } from "@/nav";

function useCloseSheetOnNavigate(pathname: string) {
  const { isMobile, setOpenMobile } = useSidebar();
  const previous = useRef(pathname);
  useEffect(() => {
    if (previous.current === pathname) return;
    previous.current = pathname;
    if (!isMobile) return;
    const timer = window.setTimeout(() => setOpenMobile(false), 260);
    return () => window.clearTimeout(timer);
  }, [pathname, isMobile, setOpenMobile]);
}

export function AppSidebar() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  useCloseSheetOnNavigate(pathname);

  return (
    <Sidebar variant="inset" collapsible="icon">
      <SidebarHeader className="px-2 pt-3 pb-3">
        <Link
          to="/"
          aria-label={`${APP_NAME} home`}
          className="group/logo flex h-8 items-center rounded-[10px] focus-visible:ring-offset-sidebar group-data-[collapsible=icon]:w-8"
        >
          <Wordmark />
        </Link>
      </SidebarHeader>
      <SidebarContent>
        <nav aria-label="Main">
          <SidebarGroup className="px-2 pt-1">
            <SidebarGroupLabel>Navigation</SidebarGroupLabel>
            <SidebarMenu>
              {NAV_ITEMS.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton tooltip={item.title} isActive={pathname === item.to} render={<Link to={item.to} />}>
                    <item.icon />
                    <SidebarMenuLabel>{item.title}</SidebarMenuLabel>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroup>
        </nav>
      </SidebarContent>
      <SidebarFooter className="p-2">
        <NavUser />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}
