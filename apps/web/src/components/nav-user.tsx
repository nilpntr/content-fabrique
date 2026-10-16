import { CircleUserIcon, LogOutIcon, MoonIcon, SettingsIcon, SunIcon } from "lucide-react";
import type { CSSProperties } from "react";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import { toggleTheme, useTheme } from "@/hooks/use-theme";
import { hue, initials } from "@/lib/utils";

const USER = { name: "Jane Doe", email: "jane@example.com" };

function ChevronsGlyph() {
  const part = "transition-[translate] duration-(--dur-spring) ease-spring";
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden className="ml-auto size-4 shrink-0 text-subtle-foreground">
      <path
        d="M5.25 6.5 8 3.9l2.75 2.6"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
        className={`${part} group-hover/menu-button:-translate-y-px group-data-popup-open/menu-button:-translate-y-0.5`}
      />
      <path
        d="M5.25 9.5 8 12.1l2.75-2.6"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
        className={`${part} group-hover/menu-button:translate-y-px group-data-popup-open/menu-button:translate-y-0.5`}
      />
    </svg>
  );
}

export function NavUser() {
  const { isMobile } = useSidebar();
  const theme = useTheme();
  const collapsedFade =
    "transition-opacity delay-90 duration-(--dur-base) group-data-[collapsible=icon]:pointer-events-none group-data-[collapsible=icon]:shrink-0 group-data-[collapsible=icon]:opacity-0 group-data-[collapsible=icon]:delay-0 group-data-[collapsible=icon]:duration-(--dur-fast)";

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<SidebarMenuButton size="lg" tooltip={USER.name} className="h-11 gap-2.5 px-1 text-foreground" />}
          >
            <UserAvatar className="size-6" />
            <div className={`grid min-w-0 flex-1 text-left leading-tight ${collapsedFade}`}>
              <span className="truncate text-[13px] font-medium tracking-[-0.01em]">{USER.name}</span>
              <span className="truncate text-[11.5px] text-subtle-foreground">{USER.email}</span>
            </div>
            <span className={`ml-auto flex ${collapsedFade}`}>
              <ChevronsGlyph />
            </span>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="min-w-60" side={isMobile ? "top" : "right"} align="end" sideOffset={isMobile ? 6 : 12}>
            <DropdownMenuGroup>
              <div className="flex items-center gap-2.5 px-2 pt-1.5 pb-2">
                <UserAvatar className="size-8" />
                <div className="grid min-w-0 flex-1 leading-tight">
                  <span className="truncate text-[13px] font-medium tracking-[-0.01em]">{USER.name}</span>
                  <span className="truncate text-[11.5px] text-subtle-foreground">{USER.email}</span>
                </div>
              </div>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem>
                <CircleUserIcon />
                Account
              </DropdownMenuItem>
              <DropdownMenuItem>
                <SettingsIcon />
                Settings
              </DropdownMenuItem>
              <DropdownMenuItem onClick={toggleTheme}>
                {theme === "dark" ? <SunIcon /> : <MoonIcon />}
                {theme === "dark" ? "Light mode" : "Dark mode"}
              </DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem>
              <LogOutIcon />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

function UserAvatar({ className }: { className?: string }) {
  return (
    <Avatar className={className} style={{ "--avatar-hue": hue(USER.name) } as CSSProperties}>
      <AvatarFallback>{initials(USER.name)}</AvatarFallback>
    </Avatar>
  );
}
