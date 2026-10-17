import { Check } from "lucide-react";
import * as React from "react";
import { DeviceFrame } from "@/components/device-frame";
import { cn } from "@/lib/utils";
import { FORMATS } from "./formats";
import type { Format } from "./types";

/**
 * One card per output format, as native radios sharing one name: the browser does the arrow keys (they move the
 * selection and the focus, and wrap), Space and the tab stop. Only Home and End are added here, and only when no
 * modifier key is held.
 */
export function FormatStep({ value, onChange }: { value: Format; onChange: (format: Format) => void }) {
  const id = React.useId();
  const refs = React.useRef(new Map<Format, HTMLInputElement>());

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return;
    const target = event.key === "Home" ? FORMATS[0] : event.key === "End" ? FORMATS.at(-1) : undefined;
    if (!target) return;
    event.preventDefault();
    onChange(target.id);
    refs.current.get(target.id)?.focus();
  }

  return (
    <fieldset className="grid min-w-0 grid-cols-2 gap-3 lg:grid-cols-4">
      <legend className="sr-only">Output format</legend>
      {FORMATS.map((format, index) => {
        const checked = format.id === value;
        const labelId = `${id}-${index}-label`;
        const detailId = `${id}-${index}-detail`;
        return (
          <label
            key={format.id}
            className={cn(
              "group/card relative flex cursor-pointer flex-col items-center gap-4 rounded-2xl bg-surface-raised px-3 pt-6 pb-4 text-center shadow-soft ring-1 ring-border select-none",
              "transition-[box-shadow,background-color] duration-(--dur-base) ease-out hover:ring-foreground/25",
              "has-focus-visible:ring-2 has-focus-visible:ring-brand has-focus-visible:ring-offset-2 has-focus-visible:ring-offset-background",
              "has-checked:bg-brand-wash has-checked:ring-2 has-checked:ring-brand",
            )}
          >
            <input
              ref={(el) => {
                if (el) refs.current.set(format.id, el);
                else refs.current.delete(format.id);
              }}
              type="radio"
              name={id}
              value={format.id}
              checked={checked}
              aria-labelledby={labelId}
              aria-describedby={detailId}
              onChange={() => onChange(format.id)}
              onKeyDown={onKeyDown}
              className="sr-only"
            />
            <span
              aria-hidden="true"
              className={cn(
                "absolute top-2.5 right-2.5 flex size-5 items-center justify-center rounded-full bg-brand text-primary-foreground",
                "scale-50 opacity-0 transition-[scale,opacity] duration-(--dur-base) ease-out",
                "group-has-checked/card:scale-100 group-has-checked/card:opacity-100 motion-safe:group-has-checked/card:duration-(--dur-spring) motion-safe:group-has-checked/card:ease-spring",
              )}
            >
              <Check className="size-3" strokeWidth={3} />
            </span>

            <span className="flex h-40 items-end justify-center">
              <span className="flex origin-bottom transition-transform duration-(--dur-base) ease-out motion-safe:group-hover/card:-translate-y-1 motion-safe:group-hover/card:scale-[1.04] motion-safe:group-has-checked/card:-translate-y-1.5 motion-safe:group-has-checked/card:scale-[1.07] motion-safe:group-has-checked/card:duration-(--dur-spring) motion-safe:group-has-checked/card:ease-spring">
                <DeviceFrame kind={format.device} postRatio={format.postRatio} active={checked} className="drop-shadow-sm" />
              </span>
            </span>

            <span className="space-y-0.5">
              <span id={labelId} className="block text-lg font-semibold tracking-[-0.02em] tabular-nums">
                {format.id}{" "}
                <span className="ml-0.5 text-[13px] font-medium tracking-normal text-muted-foreground">{format.name}</span>
              </span>
              <span id={detailId} className="block text-[13px] text-muted-foreground">
                <span className="tabular-nums">{format.size}</span>
                <span aria-hidden="true"> · </span>
                <span className="sr-only">, </span>
                {format.use}
              </span>
            </span>
          </label>
        );
      })}
    </fieldset>
  );
}
