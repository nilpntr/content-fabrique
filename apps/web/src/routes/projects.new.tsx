import { createFileRoute } from "@tanstack/react-router";
import { NewProjectPage } from "@/pages/new-project";

// Protected like every route: the root route's guard needs a signed-in user with an active workspace.
export const Route = createFileRoute("/projects/new")({ component: NewProjectPage });
