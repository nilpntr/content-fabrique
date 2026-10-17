import { Info } from "lucide-react";
import { type DemoState, LibraryDemo } from "@/components/library/library-demo";

/** The library on sample data. The upload and the API arrive with P1-01 and P1-02. */
export function LibraryPage({ state, localSamples }: { state?: DemoState; localSamples?: boolean }) {
  return (
    <div className="space-y-6">
      <div className="animate-rise">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">Library</h1>
        <p className="mt-1.5 max-w-prose text-muted-foreground">Your footage. Hover a clip to preview it, and move across it to scrub.</p>
      </div>
      <p role="note" className="flex w-fit items-center gap-2 rounded-[10px] bg-wash px-3 py-2 text-[12.5px] text-muted-foreground shadow-[inset_0_0_0_1px_var(--border)]">
        <Info aria-hidden="true" className="size-4 shrink-0 text-subtle-foreground" />
        Sample clips until upload lands (P1-01/P1-02)
      </p>
      <LibraryDemo state={state} localSamples={localSamples} />
    </div>
  );
}
