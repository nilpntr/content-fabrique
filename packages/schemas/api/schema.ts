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
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        /**
         * @description The stable error codes of the API. The web client branches on these; new ones may be added, so treat an unknown
         *     code as a generic failure of its HTTP status.
         * @enum {string}
         */
        ErrorCode: "invalid_request" | "unauthenticated" | "invalid_token" | "plan_required" | "no_active_workspace" | "user_deleted" | "workspace_deleted" | "not_a_member" | "origin_not_allowed" | "invalid_signature" | "invalid_event" | "payload_too_large" | "internal_error" | "identity_unavailable" | "auth_not_configured" | "queue_not_migrated" | "webhook_not_configured";
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
         * @description Signed in, but not allowed: `no_active_workspace` (no organization is active), `user_deleted`, `workspace_deleted`
         *     or `not_a_member` (removed from the workspace).
         */
        Forbidden: {
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
         *     server) or `queue_not_migrated` (River's tables are missing, so a new workspace cannot be created). Try again later.
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
    parameters: never;
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
}
