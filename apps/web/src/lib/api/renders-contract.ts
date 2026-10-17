/**
 * TEMPORARY: the renders part of the API contract (P3-06), written by hand because P3-06 is not merged yet and
 * `packages/schemas/openapi.yaml` does not have it. It mirrors the contract of branch `p3-06-render-jobs`, in the shape that
 * `openapi-typescript` generates, so that client.ts can type the calls with it (`paths & RenderPaths`).
 *
 * When P3-06 lands and `task gen` produces these types: delete this file, remove `RenderPaths` from client.ts and point the
 * `Render*` aliases there at `components["schemas"]`. Nothing else in the app changes.
 */
import type { components } from "@cf/schemas/api";

type ProjectId = components["schemas"]["ProjectId"];

export type RenderId = string;
export type RenderProfile = "preview" | "final";
export type RenderStatus = "queued" | "rendering" | "ready" | "failed";

export interface RenderFile {
  /** A presigned GET URL. */
  url: string;
  content_type: string;
  size_bytes: number;
}

export interface ContactSheetTile {
  index: number;
  out_ms: number;
  frame: number;
  item_id?: string;
  kind: "shot" | "last";
}

export interface ContactSheet {
  columns: number;
  tile_width: number;
  tile_height: number;
  tiles: ContactSheetTile[];
}

export interface RenderFiles {
  mp4: RenderFile;
  contact_sheet: RenderFile;
  /** A JSON array of `{ out_start_ms, out_end_ms, item_id, asset_id?, source_in_ms?, speed? }`. */
  time_map: RenderFile;
}

export interface Render {
  id: RenderId;
  project_id: ProjectId;
  timeline_version: number;
  profile: RenderProfile;
  status: RenderStatus;
  /** 0 to 100. */
  progress: number;
  stage: string | null;
  duration_ms: number | null;
  width?: number | null;
  height?: number | null;
  size_bytes: number | null;
  contact_sheet: ContactSheet | null;
  sound_cues: unknown[];
  /** Null until ready, and in lists. */
  files: RenderFiles | null;
  urls_expires_at: string | null;
  failure_reason: string | null;
  requested_by: { kind: "user" | "agent"; id: string };
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface ListRendersResponse {
  items: Render[];
  next_cursor: string | null;
}

export interface CreateRenderRequest {
  version: number;
  profile: RenderProfile;
}

type Ok<T> = { headers: { [name: string]: unknown }; content: { "application/json": T } };

export interface RenderPaths {
  "/v1/projects/{id}/renders": {
    parameters: { query?: never; header?: never; path: { id: ProjectId }; cookie?: never };
    get: {
      parameters: {
        query?: { limit?: number; cursor?: string; version?: number; profile?: RenderProfile };
        header?: never;
        path: { id: ProjectId };
        cookie?: never;
      };
      requestBody?: never;
      responses: { 200: Ok<ListRendersResponse> };
    };
    put?: never;
    post: {
      parameters: { query?: never; header?: never; path: { id: ProjectId }; cookie?: never };
      requestBody: { content: { "application/json": CreateRenderRequest } };
      responses: { 200: Ok<Render>; 202: Ok<Render> };
    };
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/renders/{id}": {
    parameters: { query?: never; header?: never; path: { id: RenderId }; cookie?: never };
    get: {
      parameters: { query?: never; header?: never; path: { id: RenderId }; cookie?: never };
      requestBody?: never;
      responses: { 200: Ok<Render> };
    };
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
}
