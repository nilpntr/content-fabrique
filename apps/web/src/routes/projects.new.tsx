import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { rememberRun } from "@/components/project/last-run";
import { NewProjectPage } from "@/pages/new-project";

// Protected like every route: the root route's guard needs a signed-in user with an active workspace.
export const Route = createFileRoute("/projects/new")({ component: NewProjectRoute });

function NewProjectRoute() {
  const navigate = useNavigate();
  return (
    <NewProjectPage
      onOpenProject={(projectId, runId) => {
        if (runId) rememberRun(projectId, runId);
        void navigate({ to: "/projects/$projectId", params: { projectId }, search: runId ? { run: runId } : {} });
      }}
    />
  );
}
