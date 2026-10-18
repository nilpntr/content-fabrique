import { useLocation, useMatches } from "@tanstack/react-router";
import { FilmIcon, FolderOpenIcon, HouseIcon, type LucideIcon, PlusIcon, SettingsIcon } from "lucide-react";
import type { FileRouteTypes } from "@/routeTree.gen";

export interface NavItem {
  title: string;
  /** Generated from the route tree, so a typo or a removed route fails typecheck. */
  to: FileRouteTypes["to"];
  icon: LucideIcon;
}

/** Only pages that do something. A new page earns a place here once it shows real data. */
export const NAV_ITEMS: readonly NavItem[] = [
  { title: "Home", to: "/", icon: HouseIcon },
  { title: "Projects", to: "/projects", icon: FolderOpenIcon },
  { title: "New video", to: "/projects/new", icon: PlusIcon },
  { title: "Library", to: "/library", icon: FilmIcon },
  { title: "Settings", to: "/settings", icon: SettingsIcon },
];

/** Whether a matched route is the nav item's route or one of its children (`/projects/$id` under `/projects`). */
export function matchesNavItem(item: NavItem, routeId: string): boolean {
  const id = routeId.length > 1 ? routeId.replace(/\/$/, "") : routeId;
  return id === item.to || (item.to !== "/" && id.startsWith(`${item.to}/`));
}

const trimSlash = (pathname: string) => (pathname.length > 1 ? pathname.replace(/\/$/, "") : pathname);

/**
 * The nav item for the current location, from the routes the router actually matched: trailing slashes do not
 * matter, and child routes keep their parent's item active. The router also "matches" the parent of a path no route
 * has (`/settings/nope` matches `/settings`, then renders Not found), so the deepest match must cover the whole
 * pathname; otherwise no item is active. Call it once (in the shell) and pass the result down.
 */
export function useActiveNavItem(): NavItem | undefined {
  const matches = useMatches();
  const pathname = useLocation({ select: (location) => location.pathname });
  const leaf = matches.at(-1);
  if (!leaf || trimSlash(leaf.pathname) !== trimSlash(pathname)) return undefined;
  // The most specific item wins: `/projects/new` is under `/projects`, and belongs to "New video".
  return NAV_ITEMS.filter((item) => matches.some((match) => matchesNavItem(item, match.routeId))).sort((a, b) => b.to.length - a.to.length)[0];
}
