import { Lightbulb, Music } from "lucide-react";
import * as React from "react";
import { ExtraCarefulOption } from "@/components/extra-careful";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

export const BRIEF_MAX_LENGTH = 2000;

/** Fitness examples; a click puts one in the brief. */
export const EXAMPLE_PROMPTS = [
  "Start with my best back pose, title 'How to grow a back 101', then the exercise clip, cut to a trending song",
  "Show the full set in 30 seconds, add rep counts as captions, and end on the lockout",
  "Before and after: open on the old mirror check, cut to today's pose with the title 'Six months in'",
  "A 15 second form check: slow motion on the hardest part of the lift, with a caption for the cue",
] as const;

/** Adds an example to the brief: it becomes the brief when that is empty, else it goes on a new paragraph. */
export function withExample(brief: string, example: string): string {
  const current = brief.trimEnd();
  return current === "" ? example : `${current}\n\n${example}`;
}

export const TITLE_MAX_LENGTH = 255;

export function BriefStep({
  value,
  onChange,
  title = "",
  onTitleChange,
  extraCareful,
  careful = false,
  onCarefulChange,
}: {
  value: string;
  onChange: (brief: string) => void;
  /** The project name; the field shows when `onTitleChange` is given. */
  title?: string;
  onTitleChange?: (title: string) => void;
  /** What an extra careful first edit costs against a standard one; the option shows when it is given (the plan includes it). */
  extraCareful?: { extra: number; standard: number };
  careful?: boolean;
  onCarefulChange?: (careful: boolean) => void;
}) {
  const titleId = React.useId();
  const textareaRef = React.useRef<HTMLTextAreaElement>(null);
  const textareaId = React.useId();
  const hintId = React.useId();
  const counterId = React.useId();

  function applyExample(example: string) {
    const next = withExample(value, example);
    if (next.length > BRIEF_MAX_LENGTH) return;
    onChange(next);
    textareaRef.current?.focus();
  }

  const remaining = BRIEF_MAX_LENGTH - value.length;
  // Said aloud only when it changes bucket, so it does not talk on every keystroke.
  const limitNote =
    remaining <= 0 ? "Character limit reached" : remaining <= 50 ? "Fewer than 50 characters left" : remaining <= 200 ? "Fewer than 200 characters left" : "";

  return (
    <div className="space-y-6">
      {onTitleChange ? (
        <div className="space-y-2">
          <label htmlFor={titleId} className="block font-medium">
            Name <span className="font-normal text-subtle-foreground">(optional)</span>
          </label>
          <Input id={titleId} value={title} maxLength={TITLE_MAX_LENGTH} placeholder="For example: Back day" onChange={(event) => onTitleChange(event.target.value)} className="sm:max-w-md" />
        </div>
      ) : null}
      <div className="space-y-2">
        <div className="flex items-baseline justify-between gap-3">
          <label htmlFor={textareaId} className="font-medium">
            What you want <span className="font-normal text-subtle-foreground">(optional)</span>
          </label>
          <span id={counterId} className={cn("text-[12.5px] tabular-nums", remaining <= 200 ? "text-foreground" : "text-subtle-foreground")}>
            {value.length} / {BRIEF_MAX_LENGTH}
            <span className="sr-only"> characters</span>
          </span>
          <span role="status" className="sr-only">
            {limitNote}
          </span>
        </div>
        <Textarea
          ref={textareaRef}
          id={textareaId}
          aria-describedby={`${hintId} ${counterId}`}
          value={value}
          maxLength={BRIEF_MAX_LENGTH}
          rows={6}
          placeholder="Which clip opens, any titles, the pace, the music."
          onChange={(event) => onChange(event.target.value)}
        />
        <p id={hintId} className="text-[13px] text-muted-foreground">
          Leave it empty and the editor picks a cut from your clips.
        </p>
      </div>

      {extraCareful && onCarefulChange ? <ExtraCarefulOption price={extraCareful} checked={careful} onChange={onCarefulChange} /> : null}

      <div className="space-y-2">
        <h3 className="font-medium">Examples</h3>
        <ul aria-label="Examples" className="grid gap-2 sm:grid-cols-2">
          {EXAMPLE_PROMPTS.map((example) => {
            const fits = withExample(value, example).length <= BRIEF_MAX_LENGTH;
            return (
              <li key={example} className="flex">
                <button
                  type="button"
                  disabled={!fits}
                  onClick={() => applyExample(example)}
                  className="w-full cursor-pointer rounded-xl bg-surface-raised px-3 py-2 text-left text-[13px] leading-5 text-muted-foreground shadow-raised transition-[color,box-shadow,translate] duration-(--dur-fast) ease-out hover:text-foreground hover:shadow-pop disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:text-muted-foreground disabled:hover:shadow-raised motion-safe:hover:-translate-y-px motion-safe:disabled:hover:translate-y-0"
                >
                  {example}
                  {fits ? null : <span className="mt-0.5 block text-[12px] text-subtle-foreground">Not enough room left</span>}
                </button>
              </li>
            );
          })}
        </ul>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <ComingSoon icon={<Lightbulb aria-hidden="true" className="size-4" />} label="Idea" placeholder="Pick a trend idea" />
        <ComingSoon icon={<Music aria-hidden="true" className="size-4" />} label="Sound" placeholder="Pick a sound" />
      </div>
    </div>
  );
}

function ComingSoon({ icon, label, placeholder }: { icon: React.ReactNode; label: string; placeholder: string }) {
  const id = React.useId();
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="flex items-center gap-1.5 font-medium text-muted-foreground">
        {icon}
        {label}
        <span className="rounded-full bg-wash px-2 py-0.5 text-[11.5px] font-medium text-subtle-foreground">Coming soon</span>
      </label>
      <Input id={id} disabled placeholder={placeholder} />
    </div>
  );
}
