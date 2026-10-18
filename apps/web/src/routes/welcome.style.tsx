import { createFileRoute } from "@tanstack/react-router";
import { WelcomeStylePage } from "@/pages/welcome-style";

// The optional onboarding step "Show me your style" for a new workspace (P6-05b). Protected like every route.
export const Route = createFileRoute("/welcome/style")({ component: WelcomeStylePage });
