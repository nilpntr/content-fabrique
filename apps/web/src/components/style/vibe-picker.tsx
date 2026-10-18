import { Check, Flame, Sparkles, Waves } from "lucide-react";
import * as React from "react";
import type { EditVibe } from "@/lib/api/client";
import { VIBES } from "@/lib/style-words";
import { cn } from "@/lib/utils";

const ICONS: Record<EditVibe, typeof Waves> = { calm: Waves, lively: Sparkles, hype: Flame };

/**
 * Calm, Lively or Hype as three cards: native radios sharing one name (the browser does the arrow keys and the tab stop). On a phone
 * the cards stack; from `sm` up they sit in a row.
 */
export function VibePicker({
  value,
  onChange,
  legend = "Vibe",
  note,
  disabled = false,
  className,
}: {
  value: EditVibe;
  onChange: (vibe: EditVibe) => void;
  /** Read out for the group; shown when `showLegend`. */
  legend?: string;
  /** A quiet line under the cards, such as where the current choice came from. */
  note?: React.ReactNode;
  disabled?: boolean;
  className?: string;
}) {
  const name = React.useId();
  return (
    <fieldset className={cn("m-0 min-w-0 border-0 p-0", className)} disabled={disabled}>
      <legend className="sr-only">{legend}</legend>
      <div className="grid gap-2 sm:grid-cols-3">
        {VIBES.map((vibe) => {
          const Icon = ICONS[vibe.value];
          const hintId = `${name}-${vibe.value}-hint`;
          return (
            <label
              key={vibe.value}
              className={cn(
                "group/vibe relative flex cursor-pointer items-center gap-3 rounded-xl bg-surface-raised px-3.5 py-3 shadow-soft ring-1 ring-border select-none sm:flex-col sm:items-start sm:gap-2 sm:py-3.5",
                "transition-[box-shadow,background-color] duration-(--dur-base) ease-out hover:ring-foreground/25",
                "has-focus-visible:ring-2 has-focus-visible:ring-brand has-focus-visible:ring-offset-2 has-focus-visible:ring-offset-background",
                "has-checked:bg-brand-wash has-checked:ring-2 has-checked:ring-brand has-disabled:cursor-not-allowed has-disabled:opacity-60",
              )}
            >
              <input
                type="radio"
                name={name}
                value={vibe.value}
                checked={vibe.value === value}
                aria-describedby={hintId}
                onChange={() => onChange(vibe.value)}
                className="sr-only"
              />
              <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-wash text-muted-foreground transition-colors group-has-checked/vibe:bg-brand group-has-checked/vibe:text-primary-foreground">
                <Icon className="size-4.5" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block font-semibold tracking-[-0.01em]">{vibe.label}</span>
                <span id={hintId} className="block text-[13px] leading-snug text-muted-foreground text-pretty">
                  {vibe.hint}
                </span>
              </span>
              <span
                aria-hidden
                className="flex size-5 shrink-0 scale-50 items-center justify-center rounded-full bg-brand text-primary-foreground opacity-0 transition-[scale,opacity] duration-(--dur-base) group-has-checked/vibe:scale-100 group-has-checked/vibe:opacity-100 sm:absolute sm:top-2.5 sm:right-2.5"
              >
                <Check className="size-3" strokeWidth={3} />
              </span>
            </label>
          );
        })}
      </div>
      {note ? <p className="mt-2 text-[13px] text-muted-foreground">{note}</p> : null}
    </fieldset>
  );
}
