import { useMatchRoute } from "@tanstack/react-router";
import { BarChart3Icon, LayoutDashboardIcon, type LucideIcon, SettingsIcon } from "lucide-react";
import type { FileRouteTypes } from "@/routeTree.gen";

export interface NavItem {
  title: string;
  /** Generated from the route tree, so a typo or a removed route fails typecheck. */
  to: FileRouteTypes["to"];
  icon: LucideIcon;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { title: "Overview", to: "/", icon: LayoutDashboardIcon },
  { title: "Reports", to: "/reports", icon: BarChart3Icon },
  { title: "Settings", to: "/settings", icon: SettingsIcon },
];

/**
 * The nav item for the current location, by the router's own route matching: trailing slashes do not matter,
 * and child routes (`/reports/123`) keep their parent's item active. `/` only matches itself.
 */
export function useActiveNavItem(): NavItem | undefined {
  const matchRoute = useMatchRoute();
  return NAV_ITEMS.find((item) => matchRoute({ to: item.to, fuzzy: item.to !== "/" }));
}
