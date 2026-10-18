import { OrganizationList } from "@clerk/tanstack-react-start";
import { createFileRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";

// Membership is required: every signed-in user works in a workspace (a Clerk organization). A user whose session has
// none active (the route guard sends them here) picks or creates one, and the app opens as soon as one is active.
// It is not an empty page: it is the first step for a new account, and the route guard depends on it.
export const Route = createFileRoute("/workspace")({ component: WorkspacePicker });

function WorkspacePicker(): ReactNode {
  return (
    <div className="grid max-w-md justify-items-center gap-4 text-center">
      <div className="space-y-1.5">
        <h1 className="text-[1.375rem] font-semibold tracking-[-0.02em]">Set up your workspace</h1>
        <p className="text-muted-foreground text-pretty">Your videos and clips live in a workspace. Give yours a name (your brand, or just your own name), or pick one you've been invited to.</p>
      </div>
      <OrganizationList hidePersonal afterSelectOrganizationUrl="/" afterCreateOrganizationUrl="/" />
    </div>
  );
}
