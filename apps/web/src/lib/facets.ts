/**
 * The clip labels (the API's `facets`): a few fixed facets, each with a short list of values, in place of the old free-form tags. The
 * vocabulary lives on the server (config/tags/v1.yaml); a label carries its own words (`label`), so nothing here hard-codes a value.
 * The app filters by three of the facets and shows at most two labels on a card.
 */

/** One label of a clip, as the API sends it. */
export interface Facet {
  facet: string;
  value: string;
  label: string;
}

/** The facets the library and the clip picker filter by, in the order they are shown, with the creator's words for them. */
export const FILTER_FACETS = [
  { id: "body", label: "Body part", any: "Body part" },
  { id: "type", label: "Type", any: "Type" },
  { id: "angle", label: "Angle", any: "Angle" },
] as const;

export type FilterFacetId = (typeof FILTER_FACETS)[number]["id"];

/** The value picked for each filter; a facet that is not there is not filtered. */
export type FacetSelection = Partial<Record<FilterFacetId, string>>;

/** A value a filter offers: the clips that have it, given the other filters. */
export interface FacetOption {
  value: string;
  label: string;
  count: number;
}

export type FacetOptions = Partial<Record<FilterFacetId, FacetOption[]>>;

/** The options of the filters from GET /v1/assets/facets (the server counts the whole library), most first. */
export function optionsFromSummaries(summaries: ReadonlyArray<{ facet: string; values: ReadonlyArray<FacetOption> }> | undefined): FacetOptions {
  const out: FacetOptions = {};
  for (const { id } of FILTER_FACETS) {
    const values = summaries?.find((s) => s.facet === id)?.values ?? [];
    if (values.length > 0) out[id] = [...values].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }
  return out;
}

const has = (facets: readonly Facet[], facet: string, value: string) => facets.some((f) => f.facet === facet && f.value === value);

/** Whether a clip's labels pass every filter of the selection. */
export function matchesFacets(facets: readonly Facet[], selection: FacetSelection, except?: FilterFacetId): boolean {
  return FILTER_FACETS.every(({ id }) => id === except || !selection[id] || has(facets, id, selection[id] as string));
}

/** Whether any filter is set. */
export const hasSelection = (selection: FacetSelection): boolean => FILTER_FACETS.some(({ id }) => Boolean(selection[id]));

/**
 * What each filter offers over a list of clips: the values the clips have, with how many have each, most first. A filter's own
 * choice is left out of its counts (so it can be switched) and the other filters narrow them, the same rule as GET /v1/assets/facets.
 */
export function countFacets(items: ReadonlyArray<{ facets: readonly Facet[] }>, selection: FacetSelection): FacetOptions {
  const out: FacetOptions = {};
  for (const { id } of FILTER_FACETS) {
    const counts = new Map<string, FacetOption>();
    for (const item of items) {
      if (!matchesFacets(item.facets, selection, id)) continue;
      for (const f of item.facets) {
        if (f.facet !== id) continue;
        const known = counts.get(f.value);
        counts.set(f.value, { value: f.value, label: f.label, count: (known?.count ?? 0) + 1 });
      }
    }
    if (counts.size > 0) out[id] = [...counts.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }
  return out;
}

/**
 * At most two labels for a card, the ones that tell clips apart: slow motion first (it is rare), then the body part, the type when it
 * is not plain lifting (posing, talking, b-roll), then the angle.
 */
export function cardBadges(facets: readonly Facet[]): Facet[] {
  const pick = (facet: string, accept: (f: Facet) => boolean = () => true) => facets.find((f) => f.facet === facet && accept(f));
  const ordered = [
    pick("type", (f) => f.value === "slow_motion"),
    pick("body"),
    pick("type", (f) => f.value !== "lift" && f.value !== "slow_motion"),
    pick("angle"),
  ];
  return ordered.filter((f): f is Facet => f !== undefined).slice(0, 2);
}

/** "seated cable row" as a heading: "Seated cable row". */
export const sentenceCase = (text: string): string => (text ? text.charAt(0).toUpperCase() + text.slice(1) : text);

/**
 * The heading of a clip's card: the exercise the quick look named ("Seated cable row"), unless the creator named the clip
 * themselves; else the title.
 */
export function clipHeading({ title, exercise, titleSource }: { title: string; exercise?: string; titleSource?: string }): string {
  const named = exercise?.trim();
  return titleSource !== "user" && named ? sentenceCase(named) : title;
}
