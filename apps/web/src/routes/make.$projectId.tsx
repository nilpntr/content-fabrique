import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { isRunId, rememberRun } from "@/components/project/last-run";
import { MakeResultPage } from "@/pages/make-result";

// The video of the guided flow: being made, then ready. `?run=` names the edit to follow, as on the project page.
export const Route = createFileRoute("/make/$projectId")({
  // `run` is always set, to undefined when it is not a run id: the router keeps the raw value of a param that the validator leaves out.
  validateSearch: (search: Record<string, unknown>): { run?: string | undefined } => ({ run: isRunId(search.run) ? search.run : undefined }),
  component: MakeResultRoute,
});

function MakeResultRoute() {
  const { projectId } = Route.useParams();
  const { run } = Route.useSearch();
  const navigate = useNavigate();
  return (
    <MakeResultPage
      projectId={projectId}
      runId={run}
      onRunStarted={(runId) => {
        rememberRun(projectId, runId);
        void navigate({ to: "/make/$projectId", params: { projectId }, search: { run: runId }, replace: true });
      }}
      onStartOver={() => void navigate({ to: "/make" })}
    />
  );
}
