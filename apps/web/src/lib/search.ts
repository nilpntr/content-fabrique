import type { ApiAsset, ProjectSummary } from "@/lib/api/client";

/**
 * The search of the ⌘K palette, over the lists the app already has (the first pages of projects and clips). There is no
 * `GET /v1/search` yet (P2-12), so this finds what the palette can see: a project by its title, a clip by its title, the
 * description and tags the quick look wrote (P1-04b) and its file name. Every word of the query has to be in the text.
 */

const words = (query: string): string[] => query.toLowerCase().split(/\s+/).filter(Boolean);

/** Whether every word of `query` is somewhere in `fields`. An empty query matches nothing here (the palette then shows pages). */
export function matchesQuery(query: string, ...fields: ReadonlyArray<string | readonly string[] | null | undefined>): boolean {
  const needles = words(query);
  if (needles.length === 0) return false;
  const haystack = fields
    .flatMap((field) => (Array.isArray(field) ? field : [field]))
    .filter((field): field is string => typeof field === "string")
    .join(" \n ")
    .toLowerCase();
  return needles.every((needle) => haystack.includes(needle));
}

export function searchProjects(projects: readonly ProjectSummary[], query: string, limit = 5): ProjectSummary[] {
  return projects.filter((project) => matchesQuery(query, project.title)).slice(0, limit);
}

/** Ready and unfinished clips alike; the library says which is which. Images are not clips, and are left out. */
export function searchClips(assets: readonly ApiAsset[], query: string, limit = 6): ApiAsset[] {
  return assets.filter((asset) => asset.kind === "video" && matchesQuery(query, asset.title, asset.description, asset.tags, asset.filename)).slice(0, limit);
}
