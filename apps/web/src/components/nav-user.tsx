import { UserButton, useUser } from "@clerk/tanstack-react-start";
import { MoonIcon, SunIcon } from "lucide-react";
import { SidebarMenu, SidebarMenuItem } from "@/components/ui/sidebar";
import { toggleTheme, useTheme } from "@/hooks/use-theme";
import { useMe } from "@/lib/api/hooks";

const collapsedFade =
  "transition-opacity delay-90 duration-(--dur-base) group-data-[collapsible=icon]:pointer-events-none group-data-[collapsible=icon]:hidden";

/**
 * The signed-in user: Clerk's UserButton (account, security, sign out) with the light/dark switch as one more action
 * in its menu, and next to it the name and the active workspace. The workspace name and role come from the API
 * (GET /v1/me), which makes the sidebar show what the API itself resolved from the session token.
 */
export function NavUser() {
  const { user } = useUser();
  const me = useMe();
  const theme = useTheme();
  const name = user?.fullName ?? user?.primaryEmailAddress?.emailAddress ?? "";
  const workspace = me.data?.workspace;

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <div className="flex h-11 items-center gap-2.5 px-1">
          <UserButton appearance={{ elements: { avatarBox: "size-6" } }}>
            <UserButton.MenuItems>
              <UserButton.Action
                label={theme === "dark" ? "Light mode" : "Dark mode"}
                labelIcon={theme === "dark" ? <SunIcon className="size-4" /> : <MoonIcon className="size-4" />}
                onClick={toggleTheme}
              />
            </UserButton.MenuItems>
          </UserButton>
          <div className={`grid min-w-0 flex-1 text-left leading-tight ${collapsedFade}`}>
            <span className="truncate text-[13px] font-medium tracking-[-0.01em]">{name}</span>
            <span className="truncate text-[11.5px] text-subtle-foreground" data-testid="sidebar-workspace">
              {workspace ? `${workspace.name} · ${workspace.role}` : " "}
            </span>
          </div>
        </div>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
