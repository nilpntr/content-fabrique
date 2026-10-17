import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { parseDemoState } from "@/components/library/library-demo";
import { isAssetId } from "@/lib/asset-id";
import { LibraryPage } from "@/pages/library";

// Protected like every route. `?state=loading|empty` and `?samples=local` are for trying the sample library by hand.
// `?open=<asset id>` opens that clip's quick-look sheet (the search palette links to it).
export const Route = createFileRoute("/library")({
  validateSearch: (search: Record<string, unknown>): { state: ReturnType<typeof parseDemoState>; samples: "local" | undefined; open?: string | undefined } => ({
    state: parseDemoState(search.state),
    samples: search.samples === "local" ? ("local" as const) : undefined,
    open: isAssetId(search.open) ? search.open : undefined,
  }),
  component: LibraryRoute,
});

function LibraryRoute() {
  const { state, samples, open } = Route.useSearch();
  const navigate = useNavigate();
  return (
    <LibraryPage
      state={state}
      localSamples={samples === "local"}
      openAssetId={open}
      // A new entry in the history, so Back closes the sheet.
      onOpenAsset={(id) => void navigate({ to: "/library", search: (previous) => ({ state: previous.state, samples: previous.samples, open: id ?? undefined }) })}
    />
  );
}
