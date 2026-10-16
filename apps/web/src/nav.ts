import { BarChart3Icon, LayoutDashboardIcon, SettingsIcon, type LucideIcon } from "lucide-react";

export interface NavItem {
  title: string;
  to: "/" | "/reports" | "/settings";
  icon: LucideIcon;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { title: "Overview", to: "/", icon: LayoutDashboardIcon },
  { title: "Reports", to: "/reports", icon: BarChart3Icon },
  { title: "Settings", to: "/settings", icon: SettingsIcon },
];
