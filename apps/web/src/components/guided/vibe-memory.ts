/**
 * The style picked for a guided video, kept in this browser by project. The project stores the creator's words but not the vibe
 * (the API takes it per edit), so when the first edit has to be started again ("Try again", "Make my video" on the video's own
 * screen) it is read back from here. A browser that never knew it starts the edit in the creator's usual style.
 */
import type { EditVibe } from "@/lib/api/client";

const KEY = (projectId: string) => `cf:guided-vibe:${projectId}`;
const VIBES: readonly string[] = ["calm", "lively", "hype"] satisfies EditVibe[];

export function rememberVibe(projectId: string, vibe: EditVibe | undefined): void {
  try {
    if (vibe) window.localStorage.setItem(KEY(projectId), vibe);
    else window.localStorage.removeItem(KEY(projectId));
  } catch {
    // Storage is blocked or full: the edit then follows the usual style.
  }
}

export function recallVibe(projectId: string): EditVibe | undefined {
  try {
    const value = window.localStorage.getItem(KEY(projectId));
    return value !== null && VIBES.includes(value) ? (value as EditVibe) : undefined;
  } catch {
    return undefined;
  }
}
