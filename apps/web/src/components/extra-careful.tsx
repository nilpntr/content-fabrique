import * as React from "react";
import { type EditCosts, creditsLabel } from "@/lib/credits";
import { cn } from "@/lib/utils";

/** Which edit this is: the project's first, or a later change. The server decides from the project; the screen knows it too. */
export type EditKind = "first_edit" | "revision";

/**
 * What the extra careful edit costs against a standard one, in credits, for this kind of edit; `undefined` when the plan does not
 * include it (the trial) or the API sent no prices: the option is then not shown. The numbers are the API's (`credits.edit_costs` of
 * `GET /v1/me`, from the price list), never the app's.
 */
export function extraCarefulCosts(costs: EditCosts | undefined, kind: EditKind): { extra: number; standard: number } | undefined {
  if (!costs?.max) return undefined;
  return { extra: costs.max[kind], standard: costs.standard[kind] };
}

/** The line under the option: what it does, and what it costs. */
export const extraCarefulText = (price: { extra: number; standard: number }): string =>
  `Thinks longer about every cut. Uses ${creditsLabel(price.extra)} instead of ${price.standard}.`;

/**
 * "Extra careful": one edit, thought through at the highest effort. Off by default. The caller passes the price from
 * {@link extraCarefulCosts}; without one nothing renders.
 */
export function ExtraCarefulOption({
  price,
  checked,
  onChange,
  disabled,
  className,
}: {
  price: { extra: number; standard: number } | undefined;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  className?: string;
}) {
  const id = React.useId();
  const hintId = React.useId();
  if (!price) return null;
  return (
    <div className={cn("flex items-start gap-2.5", className)}>
      <input
        id={id}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        aria-describedby={hintId}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 size-4 shrink-0 cursor-pointer accent-brand disabled:cursor-not-allowed"
      />
      <div className="space-y-0.5">
        <label htmlFor={id} className="cursor-pointer text-[13.5px] font-medium">
          Extra careful
        </label>
        <p id={hintId} className="text-[12.5px] text-muted-foreground text-pretty">
          {extraCarefulText(price)}
        </p>
      </div>
    </div>
  );
}
