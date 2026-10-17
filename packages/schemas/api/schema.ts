/**
 * This file was generated from openapi.yaml by openapi-typescript (`bun run gen:api` in packages/schemas, or `task gen:api`).
 * Do not edit it by hand: change the spec and regenerate. CI fails when this file is out of date.
 * Operations tagged `webhooks` are not part of it.
 */
export interface paths {
    "/v1/me": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * The signed-in user, the active workspace and its plan
         * @description Open to a workspace without a plan: it is how the web app finds out. A plan that cannot be read does not fail the
         *     request; it is reported as `entitlements.status = "unknown"` (never active).
         */
        get: operations["getMe"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/workspace/plan": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * The plan of the active workspace and what it includes
         * @description Open to a workspace without a plan (it answers `active: false` and zero limits).
         */
        get: operations["getWorkspacePlan"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/library/summary": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Counts of the workspace's library
         * @description The first paid-only endpoint. There is no library yet (P1), so the counts are zero; it exists to prove the paywall
         *     on the API.
         */
        get: operations["getLibrarySummary"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * List the workspace's assets, newest first
         * @description Cursor pagination. Deleted assets are not listed. A `ready` asset carries its `media` facts and short-lived
         *     `derivatives` URLs, as in `getAsset`.
         */
        get: operations["listAssets"];
        put?: never;
        /**
         * Start an upload
         * @description Creates the asset (status `uploading`) and its multipart upload, and returns presigned `PUT` URLs for the first
         *     parts. The server picks the part size (`upload.part_size`, at least 8 MiB, at most 10,000 parts) and the object key;
         *     the client never names a key. The client uploads each part with a plain `PUT` of exactly `size_bytes` bytes to its
         *     URL (no other header is needed), keeps the `ETag` response header of every part, asks for more URLs with
         *     `POST /v1/assets/{id}/parts`, and finishes with `POST /v1/assets/{id}/complete`. A part URL is valid for 15 minutes;
         *     the upload as a whole for 24 hours.
         *     Only owners and editors may upload. At most 25 uploads can be in progress per workspace.
         */
        post: operations["createAsset"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        /**
         * One asset
         * @description An asset of another workspace, or one that does not exist, is `404 not_found`. While the asset is `processing`,
         *     `ingest_progress` and `ingest_stage` say how far the ingest is. Once it is `ready`, `media` holds what the probe
         *     found and `derivatives` holds a presigned GET URL per generated file (see `AssetDerivatives`); the URLs stop working
         *     at `derivative_urls_expire_at`, so fetch the asset again for fresh ones.
         */
        get: operations["getAsset"];
        put?: never;
        post?: never;
        /**
         * Delete an asset
         * @description Soft-deletes the asset and enqueues the deletion of its objects. An asset whose upload is still in progress is
         *     aborted as well. Only owners and editors may delete.
         */
        delete: operations["deleteAsset"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets/{id}/upload": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        /**
         * The state of an upload, to resume it
         * @description What a client needs after a reload: the part size and count, and the parts object storage already holds
         *     (`completed_parts`, from `ListParts`: number, ETag and size). Upload the parts that are not listed, then complete.
         *     The state stays readable after the upload is completed, aborted or expired (`upload.status`); then
         *     `completed_parts` is empty.
         */
        get: operations["getAssetUpload"];
        put?: never;
        post?: never;
        /**
         * Abort an upload
         * @description Aborts the multipart upload (its parts are discarded) and removes the asset. Only owners and editors may abort.
         *     Aborting twice is `404 not_found`; an upload that is already completed is `409 upload_not_active`.
         */
        delete: operations["abortAssetUpload"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets/{id}/parts": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Presigned URLs for more parts
         * @description Up to 100 part numbers at a time, each between 1 and `upload.part_count`. Only for an upload that is in progress.
         */
        post: operations["presignAssetParts"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets/{id}/complete": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Finish an upload
         * @description Sends every part with its ETag. The server checks them against what object storage holds (all `part_count` parts,
         *     the right sizes), completes the multipart upload, checks that the object is exactly `size_bytes` long, marks the
         *     asset `uploaded` and enqueues its ingest job. Completing an upload that was already completed answers 200 again.
         *     Only owners and editors may complete.
         */
        post: operations["completeAssetUpload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        /**
         * @description The stable error codes of the API. The web client branches on these; new ones may be added, so treat an unknown
         *     code as a generic failure of its HTTP status.
         * @enum {string}
         */
        ErrorCode: "invalid_request" | "unauthenticated" | "invalid_token" | "plan_required" | "no_active_workspace" | "user_deleted" | "workspace_deleted" | "not_a_member" | "origin_not_allowed" | "invalid_signature" | "invalid_event" | "payload_too_large" | "internal_error" | "identity_unavailable" | "auth_not_configured" | "queue_not_migrated" | "webhook_not_configured" | "insufficient_role" | "not_found" | "upload_not_active" | "upload_expired" | "file_too_large" | "unsupported_content_type" | "parts_mismatch" | "size_mismatch" | "too_many_uploads" | "storage_unavailable" | "storage_not_configured";
        ErrorDetail: {
            code: components["schemas"]["ErrorCode"];
            /** @description For people. It may change; do not branch on it. */
            message: string;
        };
        /** @description The error body of every endpoint. */
        Error: {
            error: components["schemas"]["ErrorDetail"];
        };
        /** @example wks_01j9z3x7m8c4v6n2q5r0t1y8ab */
        WorkspaceId: string;
        /** @example usr_01j9z3x7m8c4v6n2q5r0t1y8ab */
        UserId: string;
        AssetId: string;
        MomentId: string;
        ShotId: string;
        AnnotationId: string;
        TrendId: string;
        ConceptId: string;
        IdeaId: string;
        ProjectId: string;
        TimelineId: string;
        RenderId: string;
        RunId: string;
        ExportId: string;
        LicenceId: string;
        UploadId: string;
        EventId: string;
        /**
         * @description The caller's role in the workspace. Clerk's `org:admin` is `owner`, `org:member` is `editor`, any other role is `viewer`.
         * @enum {string}
         */
        Role: "owner" | "editor" | "viewer";
        /**
         * @description The plans. `none` is no plan: the paywall.
         * @enum {string}
         */
        PlanName: "none" | "trial" | "studio" | "creator" | "pro";
        /**
         * @description The state of the workspace's plan record. `none` means the workspace never had a plan; `unknown` means the API
         *     could not read it (never active).
         * @enum {string}
         */
        PlanStatus: "active" | "past_due" | "canceled" | "none" | "unknown";
        /**
         * @description Who set the plan. `none` when the workspace never had one.
         * @enum {string}
         */
        PlanSource: "manual" | "merchant" | "none";
        /**
         * @description Who pays for the model calls (D-18): `managed` (we do) or `byok` (the creator's own key). Empty when the workspace
         *     has no active plan.
         * @enum {string}
         */
        AiBilling: "managed" | "byok" | "";
        /** @description The plan of the workspace as the API shows it. */
        Entitlements: {
            plan: components["schemas"]["PlanName"];
            status: components["schemas"]["PlanStatus"];
            /** @description The paywall looks at this and nothing else. */
            active: boolean;
            /**
             * Format: date-time
             * @description When the paid period ends. `null` means the plan does not end (or there is none).
             */
            current_period_end: string | null;
        };
        /** @description What the plan includes. A zero number means "not included"; all zero without an active plan. */
        PlanLimits: {
            /** @description Exported videos per month (the trial's two are in total, not per month). */
            exports: number;
            storage_gb: number;
            ai_billing: components["schemas"]["AiBilling"];
        };
        /** @description The signed-in user. */
        MeUser: {
            id: components["schemas"]["UserId"];
            email: string;
        };
        /** @description The active workspace (the Clerk organization of the session) and the caller's role in it. */
        MeWorkspace: {
            id: components["schemas"]["WorkspaceId"];
            name: string;
            role: components["schemas"]["Role"];
        };
        MeResponse: {
            user: components["schemas"]["MeUser"];
            workspace: components["schemas"]["MeWorkspace"];
            entitlements: components["schemas"]["Entitlements"];
        };
        /** @description The entitlements, flattened, plus where the plan came from and what it includes. */
        WorkspacePlanResponse: components["schemas"]["Entitlements"] & {
            source: components["schemas"]["PlanSource"];
            limits: components["schemas"]["PlanLimits"];
        };
        LibrarySummaryResponse: {
            assets: number;
            projects: number;
        };
        /** @enum {string} */
        AssetKind: "video" | "audio" | "image";
        /**
         * @description `uploading` (the multipart upload is in progress), `uploaded` (the object is complete; ingest is queued),
         *     `processing` (the ingest is running: see `ingest_progress`), `ready` (probed, with its derivatives), or `failed`
         *     (see `failure_reason`).
         * @enum {string}
         */
        AssetStatus: "uploading" | "uploaded" | "processing" | "ready" | "failed";
        Asset: {
            id: components["schemas"]["AssetId"];
            kind: components["schemas"]["AssetKind"];
            status: components["schemas"]["AssetStatus"];
            /** @description The file's name as the server stored it (a path and control characters removed, at most 255 bytes). */
            filename: string;
            content_type: string;
            /** Format: int64 */
            size_bytes: number;
            /** @description The SHA-256 (lowercase hex) the client claimed when it started the upload. Null when it did not say. Not verified by the upload itself. */
            sha256: string | null;
            /** @description Editable. Starts as the file name without its extension. */
            title: string;
            description: string;
            tags: string[];
            /**
             * @description Why the asset is `failed`, as a code that is safe to show. The upload failed: `upload_expired`, `size_mismatch`,
             *     `upload_aborted`. The ingest could not process the file: `unsupported_format` (not a container we accept),
             *     `corrupt_media` (damaged or cut off), `no_video_stream`, `no_audio_stream`, `undecodable_video`,
             *     `invalid_duration`, `too_long` (over 4 hours), `unsupported_resolution`, `kind_mismatch`; or `processing_failed`
             *     (our fault: it failed on every retry). Null otherwise.
             */
            failure_reason: string | null;
            /** @description How far the ingest is while `status` is `processing`; 100 when `ready`, 0 before it starts. */
            ingest_progress: number;
            /** @description What the ingest is doing while `status` is `processing` (`starting`, `downloading`, `probing`, `proxy`, `derivatives`, `uploading`). Null otherwise. */
            ingest_stage: string | null;
            media?: components["schemas"]["MediaInfo"];
            derivatives?: components["schemas"]["AssetDerivatives"];
            /**
             * Format: date-time
             * @description When the URLs in `derivatives` stop working. Present with `derivatives`.
             */
            derivative_urls_expire_at?: string;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
        };
        /** @description What the ingest probe found in the original. Present once the asset is `ready`. */
        MediaInfo: {
            /**
             * Format: int64
             * @description 0 for an image.
             */
            duration_ms: number;
            /** @description The upright (display) width of the original in pixels, rotation applied. Null for audio. */
            width: number | null;
            /** @description The upright (display) height. A phone clip held upright is portrait here although it is stored sideways. */
            height: number | null;
            /** Format: double */
            fps: number | null;
            /**
             * @description Degrees clockwise the stored picture of the original must turn to be upright. The derivatives have it applied already and carry no rotation flag.
             * @enum {integer}
             */
            rotation: 0 | 90 | 180 | 270;
            video_codec: string | null;
            /** @description The codec of the audio stream that was used. A file's other audio tracks that cannot be decoded (the APAC spatial-audio track of an iPhone clip) are ignored. */
            audio_codec: string | null;
            /** @description True when a decodable audio stream was found and is in the proxy. */
            has_audio: boolean;
            /** @description True when the original is HDR (PQ or HLG). The proxy is always SDR BT.709. */
            hdr: boolean;
        };
        DerivativeFile: {
            /** @description A presigned GET URL. It supports HTTP range requests. Treat it as a secret until it expires. */
            url: string;
            content_type: string;
            /** Format: int64 */
            size_bytes: number;
            /** @description Pixels, for pictures and videos. */
            width?: number;
            height?: number;
            /** Format: int64 */
            duration_ms?: number;
        };
        /**
         * @description The files the ingest made, each as a short-lived presigned GET URL. Only the files that exist are present: an
         *     audio file has no picture files, and a clip without a decodable audio track has no `waveform`.
         */
        AssetDerivatives: {
            /** @description `proxy_720.mp4`: H.264, 720 px on the short side (a portrait clip is 720x1280), upright with no rotation flag, SDR BT.709, a keyframe every second, the index first, AAC audio. Plays and seeks in a plain `<video>`. */
            proxy_720?: components["schemas"]["DerivativeFile"];
            /** @description `poster.jpg`: a representative frame. */
            poster?: components["schemas"]["DerivativeFile"];
            /** @description `hover_preview.mp4`: about 8 s made of four 2 s segments spread over the clip (the whole clip when it is shorter), muted, 360 px on the short side, under 1 MB. */
            hover_preview?: components["schemas"]["DerivativeFile"];
            /** @description `sprite.jpg`: a grid of 160 px wide tiles, one per `interval_sec` seconds (at most 100), for hover scrubbing. `sprite_vtt` says where each tile is. */
            sprite?: components["schemas"]["DerivativeFile"];
            /** @description `sprite.vtt`: a WebVTT thumbnails track. Each cue is `sprite.jpg#xywh=x,y,w,h`; resolve `sprite.jpg` to the `sprite` URL. */
            sprite_vtt?: components["schemas"]["DerivativeFile"];
            /** @description `waveform.json`: `{version, sample_rate, duration_ms, samples_per_peak, peaks}`, up to 1000 peaks from 0 to 1. */
            waveform?: components["schemas"]["DerivativeFile"];
        };
        CreateAssetRequest: {
            filename: string;
            /**
             * Format: int64
             * @description The exact size of the file. At most 20 GB. The upload only completes when the stored object has exactly this size.
             */
            size_bytes: number;
            /** @description `video/mp4`, `video/quicktime`, `video/webm`, `audio/*`, `image/jpeg`, `image/png` or `image/heic`. */
            content_type: string;
            /** @description Optional SHA-256 of the file, in hex. */
            sha256?: string;
        };
        /**
         * @description `active` (parts can be uploaded), `completed`, `aborted` (by the client or by the clean-up job) or `expired` (past
         *     `expires_at`, not cleaned up yet).
         * @enum {string}
         */
        UploadStatus: "active" | "completed" | "aborted" | "expired";
        UploadInfo: {
            id: components["schemas"]["UploadId"];
            asset_id: components["schemas"]["AssetId"];
            status: components["schemas"]["UploadStatus"];
            /**
             * Format: int64
             * @description Bytes in every part but the last, which holds the rest.
             */
            part_size: number;
            /** @description Part numbers run from 1 to this. */
            part_count: number;
            /**
             * Format: date-time
             * @description After this the upload can no longer be completed, and it is aborted.
             */
            expires_at: string;
        };
        PartUrl: {
            part_number: number;
            /** @description A presigned URL. `PUT` exactly `size_bytes` bytes to it; the `ETag` response header is the part's ETag. Treat it as a secret. */
            url: string;
            /**
             * Format: int64
             * @description The exact Content-Length the URL is signed for.
             */
            size_bytes: number;
        };
        CreateAssetResponse: {
            asset: components["schemas"]["Asset"];
            upload: components["schemas"]["UploadInfo"];
            /** @description Presigned URLs for the first parts (up to 20). */
            parts: components["schemas"]["PartUrl"][];
            /** Format: date-time */
            urls_expire_at: string;
        };
        PresignPartsRequest: {
            part_numbers: number[];
        };
        PresignPartsResponse: {
            parts: components["schemas"]["PartUrl"][];
            /** Format: date-time */
            urls_expire_at: string;
        };
        /** @description A part object storage holds. */
        UploadedPart: {
            part_number: number;
            etag: string;
            /** Format: int64 */
            size_bytes: number;
        };
        UploadStateResponse: {
            upload: components["schemas"]["UploadInfo"];
            completed_parts: components["schemas"]["UploadedPart"][];
        };
        CompletedPartRef: {
            part_number: number;
            etag: string;
        };
        CompleteAssetRequest: {
            parts: components["schemas"]["CompletedPartRef"][];
        };
        ListAssetsResponse: {
            items: components["schemas"]["Asset"][];
            /** @description Pass it as `cursor` for the next page. Null on the last page. */
            next_cursor: string | null;
        };
        WebhookResult: {
            /**
             * @description `applied`: the event changed something. `duplicate`: a replay of a delivery already applied. `ignored`: an event
             *     type the endpoint does not handle. `rejected`: signed by Clerk, but not usable; recorded as failed and
             *     acknowledged so that Svix does not retry it.
             * @enum {string}
             */
            status: "applied" | "duplicate" | "ignored" | "rejected";
        };
    };
    responses: {
        /**
         * @description No token (`unauthenticated`) or a token that failed verification (`invalid_token`). Sent with
         *     `WWW-Authenticate: Bearer`.
         */
        Unauthenticated: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** @description The workspace has no active plan (`plan_required`). The web app shows the paywall and fetches `/v1/me` again. */
        PlanRequired: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /**
         * @description Signed in, but not allowed: `no_active_workspace` (no organization is active), `user_deleted`, `workspace_deleted`,
         *     `not_a_member` (removed from the workspace) or `insufficient_role` (a viewer tried to change something).
         */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** @description `invalid_request`: the body, a parameter or an id is not valid. */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /**
         * @description `not_found`: no such thing in this workspace. An id that belongs to another workspace is indistinguishable from one
         *     that does not exist.
         */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** @description `upload_not_active` (the upload is already completed, aborted or failed) or `upload_expired` (its 24 hours are up). */
        UploadConflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** @description `internal_error`. Logged on the server; nothing about the cause is in the body. */
        InternalError: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /**
         * @description `identity_unavailable` (Clerk's API or key set did not answer), `auth_not_configured` (no CLERK_SECRET_KEY on the
         *     server) or `queue_not_migrated` (River's tables are missing, so a new workspace cannot be created), `storage_not_configured` (no
         *     R2_* settings on the server) or `storage_unavailable` (object storage did not answer). Try again later.
         */
        Unavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
    };
    parameters: {
        /** @description The asset's TypeID. */
        AssetIdPath: components["schemas"]["AssetId"];
    };
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getMe: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The caller. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MeResponse"];
                };
            };
            401: components["responses"]["Unauthenticated"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    getWorkspacePlan: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The plan. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["WorkspacePlanResponse"];
                };
            };
            401: components["responses"]["Unauthenticated"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    getLibrarySummary: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The counts. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["LibrarySummaryResponse"];
                };
            };
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    listAssets: {
        parameters: {
            query?: {
                /** @description How many assets to return (default 50). */
                limit?: number;
                /** @description The `next_cursor` of the previous page. Opaque. */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A page of assets. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListAssetsResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    createAsset: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateAssetRequest"];
            };
        };
        responses: {
            /** @description The asset and the first batch of part URLs. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["CreateAssetResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            /** @description `file_too_large`: more than 20 GB. */
            413: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `unsupported_content_type`: not one of the accepted media types. */
            415: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `too_many_uploads`: the workspace already has 25 uploads in progress. */
            429: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    getAsset: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The asset. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Asset"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    deleteAsset: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Deleted. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    getAssetUpload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The upload. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["UploadStateResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    abortAssetUpload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Aborted. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["UploadConflict"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    presignAssetParts: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PresignPartsRequest"];
            };
        };
        responses: {
            /** @description The URLs. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PresignPartsResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["UploadConflict"];
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    completeAssetUpload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The asset's TypeID. */
                id: components["parameters"]["AssetIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CompleteAssetRequest"];
            };
        };
        responses: {
            /** @description The asset, now `uploaded`. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Asset"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["UploadConflict"];
            /**
             * @description `parts_mismatch` (a part is missing, or its ETag or size is not what object storage holds: upload it again and
             *     complete again) or `size_mismatch` (the stored object is not `size_bytes` long; the upload failed and the asset
             *     is `failed`).
             */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
}
