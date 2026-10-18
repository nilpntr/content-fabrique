import { ArrowLeft, ArrowRight, Check } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { VibePicker } from "@/components/style/vibe-picker";
import type { EditVibe } from "@/lib/api/client";
import { vibeLabel } from "@/lib/style-words";
import { cn } from "@/lib/utils";
import { BriefStep } from "./brief-step";
import { formatDuration } from "./duration";
import { FootageStep, totalDurationMs, validPicks } from "./footage-step";
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
  { id: "format", label: "Format", title: "Where will you post it?", description: "I'll make the video the right shape for it. This can't change later." },
  { id: "footage", label: "Clips", title: "Pick your clips", description: "Choose the clips I can use. Nothing else ends up in the video." },
  { id: "brief", label: "Your idea", title: "Tell me what you want", description: "Which clip opens, a title, the pace. Or skip this and leave it to me." },
  { id: "review", label: "Review", title: "Review and start", description: "Look over your choices, then I'll start editing." },
] as const satisfies readonly StepDef[];

export interface NewProjectFlowProps {
  /** The clips in the library. */
  assets: readonly Asset[];
  /** Called with the finished choices. May be async: the button waits, and a rejection shows an error. */
  onCreate: (input: NewProjectInput) => void | Promise<void>;
  onCancel?: () => void;
  /** The format that starts selected. TikTok is the only publishing target, so it defaults to 9:16. */
  initialFormat?: Format;
  /** A poster or preview would not load (an expired URL): fetch the library again. */
  onMediaError?: () => void;
  /** The most the first edit may cost, in dollars. Shown on the review step. */
  costNote?: string;
  /** Words for a failed `onCreate`, from the error it threw. Without it, or for an error it returns nothing for, a general line is shown. */
  describeError?: (error: unknown) => string | undefined;
  /** The vibe the picker starts on: the creator's style's (lively when there is none). */
  defaultVibe?: EditVibe;
  /** Where the default vibe comes from, under the picker ("From your style"). */
  vibeNote?: string;
  /** What an extra careful first edit costs against a standard one, in credits. The option shows only when this is given (the plan includes it). */
  extraCareful?: { extra: number; standard: number };
}

/** The new-project flow: format, footage, instructions, then a review. All state lives here until `onCreate`. */
export function NewProjectFlow({
  assets,
  onCreate,
  onCancel,
  initialFormat = "9:16",
  onMediaError,
  costNote,
  describeError,
  defaultVibe = "lively",
  vibeNote,
  extraCareful,
}: NewProjectFlowProps) {
  const [stepIndex, setStepIndex] = React.useState(0);
  const [format, setFormat] = React.useState<Format>(initialFormat);
  const [pickedIds, setAssetIds] = React.useState<AssetId[]>([]);
  const [brief, setBrief] = React.useState("");
  const [careful, setCareful] = React.useState(false);
  const [creating, setCreating] = React.useState(false);
  const [title, setTitle] = React.useState("");
  // The vibe of this video: the style's until the creator picks another (only a picked one is sent).
  const [pickedVibe, setPickedVibe] = React.useState<EditVibe | null>(null);
  const vibe = pickedVibe ?? defaultVibe;
  const [failed, setFailed] = React.useState<string | false>(false);
  const headingRef = React.useRef<HTMLHeadingElement>(null);
  const createRef = React.useRef<HTMLButtonElement>(null);
  const shownStep = React.useRef(0);

  // One source for every count, the review and the payload: the picks that still exist in the library.
  const assetIds = React.useMemo(() => validPicks(assets, pickedIds), [assets, pickedIds]);
  // Forget the picks that left the library, for good.
  React.useEffect(() => {
    setAssetIds((current) => (current.length === validPicks(assets, current).length ? current : validPicks(assets, current)));
  }, [assets]);

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
    if (creating || index < 0 || index >= STEPS.length || !reachable(index)) return;
    setFailed(false);
    setStepIndex(index);
  }

  async function create() {
    if (creating || !hasClips) return;
    setCreating(true);
    setFailed(false);
    try {
      await onCreate({
        format,
        assetIds: [...assetIds],
        brief: brief.trim(),
        ...(title.trim() === "" ? {} : { title: title.trim() }),
        ...(pickedVibe && pickedVibe !== defaultVibe ? { vibe: pickedVibe } : {}),
        ...(extraCareful && careful ? { quality: "max" as const } : {}),
      });
    } catch (error) {
      setFailed(describeError?.(error) ?? "Your video could not be started. Try again.");
      // The button stayed focusable while pending; make sure the focus is on it for the retry.
      createRef.current?.focus();
    } finally {
      setCreating(false);
    }
  }

  const totalMs = totalDurationMs(assets, assetIds);

  return (
    <div className="space-y-6">
      <div className="animate-rise space-y-1.5">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">New video</h1>
        <p className="max-w-prose text-muted-foreground">Three quick steps: where it's going, which clips, and what you'd like.</p>
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
                  disabled={creating || !reachable(index)}
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

        <fieldset disabled={creating} className="m-0 min-w-0 border-0 p-0">
          {step.id === "format" ? <FormatStep value={format} onChange={setFormat} /> : null}
          {step.id === "footage" ? <FootageStep assets={assets} selectedIds={assetIds} onChange={setAssetIds} onMediaError={onMediaError} /> : null}
          {step.id === "brief" ? (
            <div className="space-y-6">
              <div className="space-y-2">
                <h3 className="font-medium">Vibe</h3>
                <VibePicker value={vibe} legend="Vibe of this video" onChange={setPickedVibe} note={pickedVibe && pickedVibe !== defaultVibe ? "Just for this video." : vibeNote} />
              </div>
              <BriefStep value={brief} onChange={setBrief} title={title} onTitleChange={setTitle} extraCareful={extraCareful} careful={careful} onCarefulChange={setCareful} />
            </div>
          ) : null}
          {step.id === "review" ? (
            <ReviewStep assets={assets} format={format} assetIds={assetIds} brief={brief} onEdit={(target) => go(STEPS.findIndex((s) => s.id === target))}
              disabled={creating}
              costNote={extraCareful && careful ? `Extra careful: the first edit uses ${extraCareful.extra} credits instead of ${extraCareful.standard}.` : costNote}
              title={title}
              vibe={vibeLabel(vibe)}
              onMediaError={onMediaError}
            />
          ) : null}
        </fieldset>
      </section>

      <p role="status" className="sr-only">
        {creating ? "Starting your video" : ""}
      </p>
      {failed ? (
        <p role="alert" className="rounded-xl bg-del-wash px-3 py-2 text-destructive">
          {failed}
        </p>
      ) : null}

      <div className="sticky bottom-0 z-20 -mx-2 flex items-center justify-between gap-3 border-t border-hairline bg-background/90 px-2 py-3 backdrop-blur-md">
        <div className="flex items-center gap-2">
          {stepIndex === 0 ? (
            onCancel ? (
              <Button variant="ghost" onClick={onCancel} disabled={creating}>
                Cancel
              </Button>
            ) : null
          ) : (
            <Button variant="ghost" onClick={() => go(stepIndex - 1)} disabled={creating}>
              <ArrowLeft aria-hidden="true" />
              Back
            </Button>
          )}
        </div>
        <div className="flex items-center gap-3">
          {step.id !== "format" ? (
            // The footage step has its own live count; this one would only repeat it for screen readers.
            <p aria-hidden={step.id === "footage" ? true : undefined} className="text-[13px] text-muted-foreground tabular-nums">
              <span className="font-medium text-foreground">
                {assetIds.length} {assetIds.length === 1 ? "clip" : "clips"}
              </span>{" "}
              · {formatDuration(totalMs)}
            </p>
          ) : null}
          {!canAdvance ? <p className="text-[13px] text-muted-foreground">Select at least one clip</p> : null}
          {isLast ? (
            // Stays focusable while pending (aria-disabled), so the focus is not lost on press.
            <Button ref={createRef} size="lg" onClick={create} disabled={creating || !hasClips} focusableWhenDisabled>
              {creating ? "Starting…" : "Make my video"}
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
