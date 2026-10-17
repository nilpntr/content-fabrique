import { ArrowLeft, ArrowRight, Check } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { BriefStep } from "./brief-step";
import { formatDuration } from "./duration";
import { FootageStep, totalDurationMs } from "./footage-step";
import { FormatStep } from "./format-step";
import { ReviewStep, type StepId } from "./review-step";
import type { Asset, AssetId, Format, NewProjectInput } from "./types";

type Step = StepId | "review";

interface StepDef {
  id: Step;
  label: string;
  title: string;
  description: string;
}

const STEPS = [
  { id: "format", label: "Format", title: "Choose a format", description: "Where will this video live? The cut is made for this size, and it can't change later." },
  { id: "footage", label: "Footage", title: "Pick your footage", description: "Choose the clips the editor may use. Nothing outside this selection ends up in the cut." },
  { id: "brief", label: "Instructions", title: "Add instructions", description: "Describe the cut you have in mind, or skip this and let the editor decide." },
  { id: "review", label: "Review", title: "Review and create", description: "Check the three choices, then start the project." },
] as const satisfies readonly StepDef[];

export interface NewProjectFlowProps {
  /** The clips in the library. */
  assets: readonly Asset[];
  /** Called with the finished choices. May be async: the button waits, and a rejection shows an error. */
  onCreate: (input: NewProjectInput) => void | Promise<void>;
  onCancel?: () => void;
  /** The format that starts selected. TikTok is the only publishing target, so it defaults to 9:16. */
  initialFormat?: Format;
}

/** The new-project flow: format, footage, instructions, then a review. All state lives here until `onCreate`. */
export function NewProjectFlow({ assets, onCreate, onCancel, initialFormat = "9:16" }: NewProjectFlowProps) {
  const [stepIndex, setStepIndex] = React.useState(0);
  const [format, setFormat] = React.useState<Format>(initialFormat);
  const [assetIds, setAssetIds] = React.useState<AssetId[]>([]);
  const [brief, setBrief] = React.useState("");
  const [creating, setCreating] = React.useState(false);
  const [failed, setFailed] = React.useState(false);
  const headingRef = React.useRef<HTMLHeadingElement>(null);
  const shownStep = React.useRef(0);

  const step = STEPS[stepIndex] ?? STEPS[0];
  const isLast = stepIndex === STEPS.length - 1;
  const hasClips = assetIds.length > 0;
  // A step can be reached once every step before it is valid. Format always is (it has a default).
  const reachable = (index: number) => index <= 1 || hasClips;
  const canAdvance = step.id !== "footage" || hasClips;

  // After a step change, move the focus to the new step's heading so keyboard and screen reader users land in it.
  React.useEffect(() => {
    if (shownStep.current === stepIndex) return;
    shownStep.current = stepIndex;
    headingRef.current?.focus();
  }, [stepIndex]);

  function go(index: number) {
    if (index < 0 || index >= STEPS.length || !reachable(index)) return;
    setFailed(false);
    setStepIndex(index);
  }

  async function create() {
    if (creating || !hasClips) return;
    setCreating(true);
    setFailed(false);
    try {
      await onCreate({ format, assetIds: [...assetIds], brief: brief.trim() });
    } catch {
      setFailed(true);
    } finally {
      setCreating(false);
    }
  }

  const totalMs = totalDurationMs(assets, assetIds);

  return (
    <div className="space-y-6">
      <div className="animate-rise space-y-1.5">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">New project</h1>
        <p className="max-w-prose text-muted-foreground">Start a job in three steps: a format, your footage, and what you want made.</p>
      </div>

      <nav aria-label="Progress">
        <ol className="flex flex-wrap gap-x-1 gap-y-2">
          {STEPS.map((item, index) => {
            const current = index === stepIndex;
            const done = index < stepIndex;
            return (
              <li key={item.id} className="flex items-center gap-1">
                <button
                  type="button"
                  disabled={!reachable(index)}
                  aria-current={current ? "step" : undefined}
                  onClick={() => go(index)}
                  className={cn(
                    "inline-flex h-8 cursor-pointer items-center gap-2 rounded-full px-2.5 text-[13.5px] font-medium transition-colors duration-(--dur-fast) disabled:cursor-default",
                    current ? "bg-wash text-foreground" : "text-muted-foreground enabled:hover:text-foreground",
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      "flex size-5 items-center justify-center rounded-full text-[11.5px] tabular-nums transition-colors duration-(--dur-fast)",
                      current || done ? "bg-brand text-primary-foreground" : "bg-wash text-subtle-foreground",
                    )}
                  >
                    {done ? <Check className="size-3" strokeWidth={3} /> : index + 1}
                  </span>
                  {item.label}
                  {done ? <span className="sr-only">, done</span> : null}
                </button>
                {index < STEPS.length - 1 ? <span aria-hidden="true" className="h-px w-4 bg-hairline sm:w-8" /> : null}
              </li>
            );
          })}
        </ol>
      </nav>

      <section key={step.id} aria-labelledby="new-project-step-title" className="animate-rise space-y-5">
        <div className="space-y-1">
          <h2 ref={headingRef} id="new-project-step-title" tabIndex={-1} className="text-lg font-semibold tracking-[-0.02em] outline-none">
            {step.title}
          </h2>
          <p className="max-w-prose text-muted-foreground">{step.description}</p>
        </div>

        {step.id === "format" ? <FormatStep value={format} onChange={setFormat} /> : null}
        {step.id === "footage" ? <FootageStep assets={assets} selectedIds={assetIds} onChange={setAssetIds} /> : null}
        {step.id === "brief" ? <BriefStep value={brief} onChange={setBrief} /> : null}
        {step.id === "review" ? (
          <ReviewStep assets={assets} format={format} assetIds={assetIds} brief={brief} onEdit={(target) => go(STEPS.findIndex((s) => s.id === target))} />
        ) : null}
      </section>

      {failed ? (
        <p role="alert" className="rounded-xl bg-del-wash px-3 py-2 text-destructive">
          The project could not be created. Try again.
        </p>
      ) : null}

      <div className="flex items-center justify-between gap-3 border-t border-hairline pt-4">
        <div className="flex items-center gap-2">
          {stepIndex === 0 ? (
            onCancel ? (
              <Button variant="ghost" onClick={onCancel}>
                Cancel
              </Button>
            ) : null
          ) : (
            <Button variant="ghost" onClick={() => go(stepIndex - 1)}>
              <ArrowLeft aria-hidden="true" />
              Back
            </Button>
          )}
        </div>
        <div className="flex items-center gap-3">
          {hasClips && step.id !== "footage" ? (
            <p className="hidden text-[13px] text-muted-foreground tabular-nums sm:block">
              {assetIds.length} {assetIds.length === 1 ? "clip" : "clips"} · {formatDuration(totalMs)}
            </p>
          ) : null}
          {!canAdvance ? <p className="text-[13px] text-muted-foreground">Select at least one clip</p> : null}
          {isLast ? (
            <Button size="lg" onClick={create} disabled={creating || !hasClips}>
              {creating ? "Creating…" : "Create project"}
            </Button>
          ) : (
            <Button onClick={() => go(stepIndex + 1)} disabled={!canAdvance}>
              {step.id === "brief" ? "Review" : "Next"}
              <ArrowRight aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
