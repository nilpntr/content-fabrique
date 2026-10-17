import { createFileRoute } from "@tanstack/react-router";
import { ProjectsPage } from "@/pages/projects";

// Protected like every route: the root route's guard needs a signed-in user with an active workspace.
export const Route = createFileRoute("/projects/")({ component: ProjectsPage });
