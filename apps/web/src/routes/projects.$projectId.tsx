import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { isRunId, rememberRun } from "@/components/project/last-run";
import { ProjectPage } from "@/pages/project";

// Protected like every route. `?run=` names the editor run to follow (a project page opened right after an edit started).
export const Route = createFileRoute("/projects/$projectId")({
  // `run` is always set, to undefined when it is not a run id: the router keeps the raw value of a param that the validator leaves out.
  validateSearch: (search: Record<string, unknown>): { run?: string | undefined } => ({ run: isRunId(search.run) ? search.run : undefined }),
  component: ProjectRoute,
});

function ProjectRoute() {
  const { projectId } = Route.useParams();
  const { run } = Route.useSearch();
  const navigate = useNavigate();
  return (
    <ProjectPage
      projectId={projectId}
      runId={run}
      onRunStarted={(runId) => {
        rememberRun(projectId, runId);
        void navigate({ to: "/projects/$projectId", params: { projectId }, search: { run: runId }, replace: true });
      }}
    />
  );
}
