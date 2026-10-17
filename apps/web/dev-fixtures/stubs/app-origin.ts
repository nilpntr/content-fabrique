/** Stands in for `src/lib/app-origin.ts`: the page's own origin. */
export async function getAppOrigin(): Promise<string> {
  return window.location.origin;
}
