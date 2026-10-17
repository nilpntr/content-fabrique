import { Lightbulb, Music } from "lucide-react";
import * as React from "react";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

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

export function BriefStep({ value, onChange }: { value: string; onChange: (brief: string) => void }) {
  const textareaRef = React.useRef<HTMLTextAreaElement>(null);
  const textareaId = React.useId();
  const hintId = React.useId();

  function applyExample(example: string) {
    onChange(withExample(value, example).slice(0, BRIEF_MAX_LENGTH));
    textareaRef.current?.focus();
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <div className="flex items-baseline justify-between gap-3">
          <label htmlFor={textareaId} className="font-medium">
            Instructions <span className="font-normal text-subtle-foreground">(optional)</span>
          </label>
          <span className="text-[12.5px] text-subtle-foreground tabular-nums">
            {value.length} / {BRIEF_MAX_LENGTH}
          </span>
        </div>
        <Textarea
          ref={textareaRef}
          id={textareaId}
          aria-describedby={hintId}
          value={value}
          maxLength={BRIEF_MAX_LENGTH}
          rows={6}
          placeholder="Tell the editor what you want: which clip opens, any titles, the pace, the music."
          onChange={(event) => onChange(event.target.value)}
        />
        <p id={hintId} className="text-[13px] text-muted-foreground">
          Leave it empty and the editor picks a cut from your clips.
        </p>
      </div>

      <div className="space-y-2">
        <h3 className="font-medium">Examples</h3>
        <ul aria-label="Example instructions" className="grid gap-2 sm:grid-cols-2">
          {EXAMPLE_PROMPTS.map((example) => (
            <li key={example} className="flex">
              <button
                type="button"
                onClick={() => applyExample(example)}
                className="w-full cursor-pointer rounded-xl bg-surface-raised px-3 py-2 text-left text-[13px] leading-5 text-muted-foreground shadow-raised transition-[color,box-shadow,translate] duration-(--dur-fast) ease-out hover:text-foreground hover:shadow-pop motion-safe:hover:-translate-y-px"
              >
                <span className="line-clamp-2">{example}</span>
              </button>
            </li>
          ))}
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
