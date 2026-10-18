import { ApiError } from "@/lib/api/client";

/**
 * What went wrong, in a sentence a creator understands, ending in the one thing to do next. Error codes are never part of these;
 * where a screen offers them at all, it is inside its "Details".
 */

const code = (error: unknown) => (error instanceof ApiError ? error.code : undefined);
const status = (error: unknown) => (error instanceof ApiError ? error.status : undefined);

const OFFLINE = "We couldn't reach the server. Check your connection and try again.";

/** The words for a refused or failed POST /v1/projects. */
export function createProjectMessage(error: unknown): string {
  if (status(error) === 402) return "You need an active plan to make videos. Choose one in Settings.";
  if (status(error) === 403) return "Your role in this workspace can't start new videos. Ask the owner to change it.";
  if (status(error) === 404) return "One of the clips you picked has been deleted. Go back to Clips and pick again.";
  if (code(error) === "assets_not_usable") {
    const detail = error instanceof ApiError && error.message !== error.code ? ` ${error.message}` : "";
    return `Some of these clips aren't ready yet, or are photos.${detail} Go back to Clips and pick others.`;
  }
  if (status(error) === 400) return "Something in your choices wasn't accepted. Check what you wrote and try again.";
  if (status(error) === 0) return OFFLINE;
  return "Your video could not be started. Try again.";
}

/** The words for a refused or failed POST /v1/projects/{id}/edit. */
export function startEditMessage(error: unknown): string {
  if (code(error) === "run_in_progress") return "I'm still working on this video. Wait until I'm done, or stop me first.";
  if (code(error) === "assets_not_usable") return "None of this video's clips can be used any more. Check that they're still in your library.";
  if (code(error) === "credits_exhausted") return "You've used up your videos for now. See your plan in Settings.";
  if (code(error) === "byok_key_required") return "Your plan uses your own AI key, and none is set up yet. Add it in Settings.";
  if (code(error) === "quality_not_in_plan") return "Extra careful edits aren't part of your plan. Turn it off and try again, or see your plan in Settings.";
  if (code(error) === "too_many_uncharged_runs") return "You've started and stopped a lot of edits today. Try again tomorrow.";
  if (status(error) === 402) return "You need an active plan to edit videos. Choose one in Settings.";
  if (status(error) === 403) return "Your role in this workspace can't edit videos. Ask the owner to change it.";
  if (code(error) === "model_not_configured") return "Editing isn't switched on yet. Try again later.";
  if (status(error) === 404) return "This video could not be found. Go back to your projects.";
  if (status(error) === 400) return "That message wasn't accepted. Make it shorter and try again.";
  if (status(error) === 429) return "Too many edits at once. Try again in a minute.";
  if (status(error) === 0) return OFFLINE;
  return "I couldn't start editing. Try again.";
}

/** The words for a refused or failed render request (a preview, or the video to download). */
export function renderMessage(error: unknown): string {
  if (code(error) === "timeline_invalid") return "This version uses a clip that has been deleted, so it can't be shown. Pick another version.";
  if (code(error) === "too_many_renders") return "A lot of videos are being prepared right now. Try again in a minute.";
  if (code(error) === "edit_not_charged" || code(error) === "version_not_charged") return "This version can't be downloaded yet: it wasn't finished by a full edit. Ask me for a change, then download the new version.";
  if (code(error) === "byok_key_required") return "Your plan uses your own AI key, and none is set up yet. Add it in Settings.";
  if (status(error) === 402) return "You need an active plan for this. Choose one in Settings.";
  if (status(error) === 403) return "Your role in this workspace can't do this. Ask the owner to change it.";
  if (status(error) === 0) return OFFLINE;
  return "The video could not be prepared. Try again.";
}

const RENDER_FAILURES: Readonly<Record<string, string>> = {
  invalid_timeline: "this version no longer fits your clips",
  source_unavailable: "a clip it uses has been deleted",
  inputs_too_large: "the clips are too large",
  render_failed: "something went wrong while making it",
  project_deleted: "the project was deleted",
  job_lost: "something went wrong on our side",
  timeout: "it took too long",
  interrupted: "it was interrupted",
  processing_failed: "something went wrong while making it",
};

/** Why a render failed, in words for the creator. */
export function renderFailureMessage(reason: string | null | undefined): string {
  return (reason ? RENDER_FAILURES[reason] : undefined) ?? "something went wrong while making it";
}

/** The words for a refused or failed write of a comment. */
export function commentMessage(error: unknown): string {
  if (status(error) === 402) return "You need an active plan to comment. Choose one in Settings.";
  if (status(error) === 403) return "You can only change your own comments, and your role must allow comments. Ask the owner if this seems wrong.";
  if (status(error) === 404) return "That comment or version is gone, so the change wasn't saved. Reload the page.";
  if (status(error) === 400) return "That comment wasn't accepted. Make it shorter and try again.";
  if (status(error) === 0) return "We couldn't reach the server, so the change wasn't saved. Check your connection and try again.";
  return "The comment could not be saved. Try again.";
}
