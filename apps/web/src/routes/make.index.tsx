import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { rememberRun } from "@/components/project/last-run";
import { MakeVideoPage } from "@/pages/make-video";

// The guided way to make a video (docs/design/guided-flow.md). Protected like every route: the root route's guard needs a signed-in user with an active workspace.
export const Route = createFileRoute("/make/")({ component: MakeVideoRoute });

function MakeVideoRoute() {
  const navigate = useNavigate();
  return (
    <MakeVideoPage
      onOpenVideo={(projectId, runId) => {
        if (runId) rememberRun(projectId, runId);
        void navigate({ to: "/make/$projectId", params: { projectId }, search: { run: runId } });
      }}
    />
  );
}
