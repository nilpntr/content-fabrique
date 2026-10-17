import { ApiError } from "@/lib/api/client";

const code = (error: unknown) => (error instanceof ApiError ? error.code : undefined);
const status = (error: unknown) => (error instanceof ApiError ? error.status : undefined);

/** The words for a refused or failed POST /v1/projects. */
export function createProjectMessage(error: unknown): string {
  if (status(error) === 402) return "Your workspace needs an active plan to start projects.";
  if (status(error) === 403) return "Your role in this workspace can't create projects. Ask an owner or editor.";
  if (status(error) === 404) return "One of the clips you picked is gone from the library. Go back to Footage and pick again.";
  if (code(error) === "assets_not_usable") {
    const detail = error instanceof ApiError ? error.message : "";
    return `Some of the clips can't be used yet: they may still be processing, or be images.${detail ? ` ${detail}` : ""} Go back to Footage and pick again.`;
  }
  if (status(error) === 400) return "The project details were not accepted. Check the instructions and try again.";
  if (status(error) === 0) return "The server could not be reached. Check your connection and try again.";
  return "The project could not be created. Try again.";
}

/** The words for a refused or failed POST /v1/projects/{id}/edit. */
export function startEditMessage(error: unknown): string {
  if (code(error) === "run_in_progress") return "The editor is still working on this project. Wait for it to finish, or stop it first.";
  if (code(error) === "assets_not_usable") return "This project has no ready video clip to edit. Check that its clips are still in your library.";
  if (status(error) === 402) return "Your workspace needs an active plan to use the editor.";
  if (status(error) === 403) return "Your role in this workspace can't start the editor. Ask an owner or editor.";
  if (code(error) === "model_not_configured") return "AI editing isn't available on this server yet.";
  if (status(error) === 404) return "This project could not be found.";
  if (status(error) === 400) return "Those instructions were not accepted. Shorten them and try again.";
  if (status(error) === 0) return "The server could not be reached. Check your connection and try again.";
  return "The editor could not be started. Try again.";
}

/** The words for a refused or failed render request. */
export function renderMessage(error: unknown): string {
  if (code(error) === "timeline_invalid") return "This version can't be rendered: a clip it uses has been deleted from the library.";
  if (code(error) === "too_many_renders") return "Too many previews are rendering at once. Try again in a minute.";
  if (status(error) === 402) return "Your workspace needs an active plan to render.";
  if (status(error) === 403) return "Your role in this workspace can't render. Ask an owner or editor.";
  if (status(error) === 0) return "The server could not be reached. Check your connection and try again.";
  return "The preview could not be started. Try again.";
}

const RENDER_FAILURES: Readonly<Record<string, string>> = {
  invalid_timeline: "this version doesn't fit the footage any more",
  source_unavailable: "a clip it uses is gone",
  inputs_too_large: "the footage is too large to preview",
  render_failed: "the video could not be made",
  project_deleted: "the project was deleted",
  job_lost: "the render was lost",
  timeout: "it took too long",
  interrupted: "it was interrupted",
  processing_failed: "the video could not be made",
};

/** Why a render failed, in words for the creator. */
export function renderFailureMessage(reason: string | null | undefined): string {
  return (reason ? RENDER_FAILURES[reason] : undefined) ?? "the video could not be made";
}

/** The words for a refused or failed write of a comment. */
export function commentMessage(error: unknown): string {
  if (status(error) === 402) return "Your workspace needs an active plan to comment.";
  if (status(error) === 403) return "Your role in this workspace can't change this comment. Only owners and editors can comment, and only the author can edit or delete a comment.";
  if (status(error) === 404) return "That comment or version is gone, so the change was not saved.";
  if (status(error) === 400) return "That comment was not accepted. Check its length and try again.";
  if (status(error) === 0) return "The server could not be reached, so the change was not saved.";
  return "The comment could not be saved. Try again.";
}
