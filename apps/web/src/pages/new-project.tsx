import { CircleCheck } from "lucide-react";
import * as React from "react";
import { formatDuration } from "@/components/new-project/duration";
import { totalDurationMs } from "@/components/new-project/footage-step";
import { NewProjectFlow } from "@/components/new-project/new-project-flow";
import { SAMPLE_ASSETS } from "@/components/new-project/sample-data";
import type { NewProjectInput } from "@/components/new-project/types";
import { notify } from "@/components/ui/toast";

export function summarize(input: NewProjectInput): string {
  const count = input.assetIds.length;
  const duration = formatDuration(totalDurationMs(SAMPLE_ASSETS, input.assetIds));
  return `${input.format}, ${count} ${count === 1 ? "clip" : "clips"} (${duration}), ${input.brief === "" ? "no instructions" : "with instructions"}`;
}

/** The new-project flow on sample data. Creating only shows what would be sent: the API arrives with P0-08. */
export function NewProjectPage() {
  const [sent, setSent] = React.useState<NewProjectInput | null>(null);

  function onCreate(input: NewProjectInput) {
    setSent(input);
    notify("Project creation arrives with the API (P0-08)", { description: `Here's what would be sent: ${summarize(input)}` });
  }

  return (
    <div className="space-y-6">
      <NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} />
      {sent ? (
        <section aria-labelledby="would-send-title" className="space-y-2 rounded-2xl bg-surface-raised p-4 shadow-soft ring-1 ring-border">
          <h2 id="would-send-title" className="flex items-center gap-2 font-semibold">
            <CircleCheck aria-hidden="true" className="size-4 text-good" />
            Project creation arrives with the API (P0-08); here's what would be sent
          </h2>
          <p className="text-muted-foreground">{summarize(sent)}</p>
          <pre className="overflow-x-auto rounded-lg bg-well p-3 font-mono text-[12.5px] leading-5 shadow-well">{JSON.stringify(sent, null, 2)}</pre>
        </section>
      ) : null}
    </div>
  );
}
