import { createFileRoute } from "@tanstack/react-router";
import { parseDemoState } from "@/components/library/library-demo";
import { LibraryPage } from "@/pages/library";

// Protected like every route. `?state=loading|empty` and `?samples=local` are for trying the sample library by hand.
export const Route = createFileRoute("/library")({
  validateSearch: (search: Record<string, unknown>) => ({
    state: parseDemoState(search.state),
    samples: search.samples === "local" ? ("local" as const) : undefined,
  }),
  component: LibraryRoute,
});

function LibraryRoute() {
  const { state, samples } = Route.useSearch();
  return <LibraryPage state={state} localSamples={samples === "local"} />;
}
