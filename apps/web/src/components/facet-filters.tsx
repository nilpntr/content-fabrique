import { ChevronDown, X } from "lucide-react";
import { type FacetOptions, type FacetSelection, FILTER_FACETS, type FilterFacetId, hasSelection } from "@/lib/facets";
import { cn } from "@/lib/utils";

/**
 * The library's and the clip picker's filters: one compact dropdown per facet ("Body part", "Type", "Angle") with the values the
 * clips have and how many have each, and a way back to all clips. A facet with nothing to offer is left out; nothing at all renders
 * nothing (a library without labels yet has no filters to show). Native selects: on a phone they open the system picker.
 */
export function FacetFilters({
  options,
  selection,
  onChange,
  className,
}: {
  options: FacetOptions;
  selection: FacetSelection;
  onChange: (selection: FacetSelection) => void;
  className?: string;
}) {
  const shown = FILTER_FACETS.filter(({ id }) => (options[id]?.length ?? 0) > 0 || selection[id]);
  if (shown.length === 0) return null;
  const set = (id: FilterFacetId, value: string) => {
    const next = { ...selection };
    if (value) next[id] = value;
    else delete next[id];
    onChange(next);
  };
  return (
    <fieldset data-slot="facet-filters" className={cn("flex min-w-0 flex-wrap items-center gap-2", className)}>
      <legend className="sr-only">Filter clips</legend>
      {shown.map(({ id, label, any }) => {
        const value = selection[id] ?? "";
        const list = options[id] ?? [];
        // A picked value the other filters leave no clip for still shows, with 0, so the choice stays visible and can be undone.
        const picked = value && !list.some((o) => o.value === value) ? [{ value, label: value.replace(/_/g, " "), count: 0 }] : [];
        return (
          <div key={id} className="relative min-w-0 flex-1 basis-[calc(33%-0.5rem)] sm:flex-none sm:basis-auto">
            <select
              aria-label={label}
              value={value}
              onChange={(event) => set(id, event.target.value)}
              data-active={Boolean(value)}
              className={cn(
                "h-9 w-full min-w-0 cursor-pointer appearance-none truncate rounded-[10px] bg-well py-0 pr-7 pl-3 text-[13.5px] text-foreground shadow-well outline-none sm:h-8.5 sm:w-auto",
                "hover:shadow-[inset_0_0_0_1px_var(--input)] focus-visible:bg-surface-raised focus-visible:shadow-[inset_0_0_0_1px_var(--brand),0_0_0_3px_var(--brand-wash)]",
                "data-[active=true]:bg-brand-wash data-[active=true]:font-medium data-[active=true]:text-brand data-[active=true]:shadow-[inset_0_0_0_1px_var(--brand)]",
              )}
            >
              <option value="">{any}</option>
              {[...list, ...picked].map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label} ({option.count})
                </option>
              ))}
            </select>
            <ChevronDown aria-hidden="true" className="pointer-events-none absolute top-1/2 right-2 size-4 -translate-y-1/2 text-subtle-foreground" />
          </div>
        );
      })}
      {hasSelection(selection) ? (
        <button
          type="button"
          onClick={() => onChange({})}
          className="inline-flex h-9 shrink-0 cursor-pointer items-center gap-1 rounded-[10px] px-2 text-[13px] font-medium text-muted-foreground hover:text-foreground sm:h-8.5"
        >
          <X aria-hidden="true" className="size-3.5" />
          Clear filters
        </button>
      ) : null}
    </fieldset>
  );
}
