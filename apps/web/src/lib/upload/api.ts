import type { components } from "@cf/schemas/api";
import { type ApiAsset, type ApiClient, ApiError, unwrap } from "@/lib/api/client";

type S = components["schemas"];
export type { ApiAsset };
export type CreateAssetRequest = S["CreateAssetRequest"];
export type CreateAssetResponse = S["CreateAssetResponse"];
export type PartUrl = S["PartUrl"];
export type PresignPartsResponse = S["PresignPartsResponse"];
export type UploadStateResponse = S["UploadStateResponse"];
export type CompletedPartRef = S["CompletedPartRef"];
export type UploadInfo = S["UploadInfo"];
export type AssetPurpose = S["AssetPurpose"];

/** The calls of the assets API that an upload makes. The manager only knows this interface. */
export interface UploadApi {
  create(request: CreateAssetRequest, signal?: AbortSignal): Promise<CreateAssetResponse>;
  state(assetId: string, signal?: AbortSignal): Promise<UploadStateResponse>;
  presign(assetId: string, partNumbers: number[], signal?: AbortSignal): Promise<PresignPartsResponse>;
  complete(assetId: string, parts: CompletedPartRef[], signal?: AbortSignal): Promise<ApiAsset>;
  abort(assetId: string): Promise<void>;
  asset(assetId: string, signal?: AbortSignal): Promise<ApiAsset>;
  /** The newest assets of a purpose (to find the one that a create call made without our hearing back). */
  recent(limit: number, signal?: AbortSignal, purpose?: AssetPurpose): Promise<ApiAsset[]>;
}

/** The upload calls over the typed client (which adds the Clerk token to each). */
export function createUploadApi(getClient: () => ApiClient): UploadApi {
  const id = (assetId: string) => assetId as S["AssetId"];
  return {
    async create(body, signal) {
      return unwrap(await getClient().POST("/v1/assets", { body, signal }));
    },
    async state(assetId, signal) {
      return unwrap(await getClient().GET("/v1/assets/{id}/upload", { params: { path: { id: id(assetId) } }, signal }));
    },
    async presign(assetId, partNumbers, signal) {
      return unwrap(
        await getClient().POST("/v1/assets/{id}/parts", { params: { path: { id: id(assetId) } }, body: { part_numbers: partNumbers }, signal }),
      );
    },
    async complete(assetId, parts, signal) {
      return unwrap(await getClient().POST("/v1/assets/{id}/complete", { params: { path: { id: id(assetId) } }, body: { parts }, signal }));
    },
    async abort(assetId) {
      unwrap(await getClient().DELETE("/v1/assets/{id}/upload", { params: { path: { id: id(assetId) } } }));
    },
    async asset(assetId, signal) {
      return unwrap(await getClient().GET("/v1/assets/{id}", { params: { path: { id: id(assetId) } }, signal }));
    },
    async recent(limit, signal, purpose) {
      return unwrap(await getClient().GET("/v1/assets", { params: { query: { limit, ...(purpose ? { purpose } : {}) } }, signal })).items;
    },
  };
}

/** Whether asking again may help: no answer, a gateway or server hiccup, a request that took too long. */
export function isTransientApiError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  if (error.code === "too_many_uploads") return false; // has its own, slower, handling
  if (error.code.endsWith("_not_configured") || error.code === "queue_not_migrated") return false;
  return error.status === 0 || error.status === 408 || error.status === 429 || error.status === 500 || (error.status >= 502 && error.status <= 504);
}
