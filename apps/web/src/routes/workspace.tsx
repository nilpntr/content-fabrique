import { OrganizationList } from "@clerk/tanstack-react-start";
import { createFileRoute } from "@tanstack/react-router";

// Membership is required: every signed-in user works in a workspace (a Clerk organization). A user whose session has
// none active (the route guard sends them here) picks or creates one, and the app opens as soon as one is active.
export const Route = createFileRoute("/workspace")({
  component: () => (
    <OrganizationList hidePersonal afterSelectOrganizationUrl="/" afterCreateOrganizationUrl="/" />
  ),
});
