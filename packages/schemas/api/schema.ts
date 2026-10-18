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
         *
         *     `credits` is the workspace's credit balance (spend control, D-31). It is left out when the balance cannot be read, which
         *     does not fail the request either.
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
         *     With `q` only the assets that match the search are listed (still newest first, same cursor): every word of `q`
         *     must occur, as the start of a word, in the asset's title, description, tags or file name, with English stemming
         *     (`row` finds "Barbell rows"). Characters other than letters and digits only separate words; a `q` without any
         *     word lists everything.
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
        /**
         * Edit an asset's title
         * @description Sets the title. The title is cleaned like every title (control characters, markup characters and extra spaces
         *     removed) and must have 1 to 255 characters left. From then on the asset's `title_source` is `user` and the quick
         *     look never overwrites the title again. Only owners and editors may edit.
         */
        patch: operations["updateAsset"];
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
    "/v1/assets/{id}/sprite.vtt": {
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
         * The hover-scrub thumbnails track, with working URLs
         * @description The WebVTT thumbnails track of a `ready` video, with every cue pointing at the sprite sheet through a fresh presigned
         *     URL (`https://...?X-Amz-...#xywh=x,y,w,h`), so a player can use it as is. The file stored in object storage
         *     (`derivatives.sprite_vtt`) names the sheet relatively and cannot work on its own, because the sheet's URL is signed and
         *     expires; this endpoint is how to get a track that works. It needs the same authentication as the rest of the API, so
         *     fetch it with the bearer token and hand the text to the player (for example as a Blob URL for a `<track>`). The cue URLs
         *     stop working at the same time as `derivative_urls_expire_at`. `404 not_found` when the asset has no sprite track (not
         *     ready yet, an audio file, an image).
         */
        get: operations["getAssetSpriteVtt"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/assets/{id}/transcript": {
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
         * The words spoken in an asset, with timings and speech structure
         * @description The transcript of an asset whose `transcript_status` is `ready` or `no_speech`. The transcription job runs after the
         *     ingest, on clips with speech only (a voice-activity check comes first), so a clip without speech has a transcript with
         *     `speech: false` and no words. Word times are milliseconds from the start of the asset. `structure` is derived from the
         *     words: sentence boundaries, filler words (um, uh, "you know" ...) and pauses longer than 400 ms, with indexes into `words`,
         *     for jump cuts and captions. `404 not_found` when the asset does not exist in the workspace or has no transcript yet
         *     (`transcript_status` is `none`, `pending` or `failed`: read it from the asset).
         */
        get: operations["getAssetTranscript"];
        put?: never;
        post?: never;
        delete?: never;
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
    "/v1/projects": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * List the workspace's projects, newest first
         * @description Cursor pagination. Deleted projects are not listed.
         */
        get: operations["listProjects"];
        put?: never;
        /**
         * Start a project
         * @description Creates a project from the new-project flow (P4-04b): the output `format`, the selected footage (`asset_ids`, 1 to 50,
         *     in the order given) and the optional `brief`. Every asset must exist in this workspace and be `ready` (probed and
         *     ingested) and a video or an audio file. An asset id that is unknown, deleted or belongs to another workspace is
         *     `404 not_found` (the same answer for all three, so an id from another workspace cannot be told from one that never
         *     existed); assets that exist but are not usable (still uploading or processing, failed, or an image) make it
         *     `422 assets_not_usable`.
         *     The project starts with no timeline: `current_timeline_version` is `null`. A timeline needs at least one clip, so
         *     version 1 is made by the first edit (`POST /v1/projects/{id}/timelines` with `base_version` 0, or the editor run).
         *     Only owners and editors may create projects.
         */
        post: operations["createProject"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/projects/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        /**
         * One project, with its footage and current timeline version
         * @description A project of another workspace, a deleted one, or one that does not exist is `404 not_found`. `assets` lists the
         *     footage in order (an asset deleted since is still listed, with `available` false). `current_version` is the summary
         *     of the latest timeline version, `null` before the first edit.
         */
        get: operations["getProject"];
        put?: never;
        post?: never;
        /**
         * Delete a project
         * @description Soft-deletes the project. Its assets stay in the library. Only owners and editors may delete.
         */
        delete: operations["deleteProject"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/projects/{id}/timelines": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        /**
         * The versions of a project's timeline, newest first
         * @description One entry per version, without the spec: its number, parent, author, the summary lines of what changed
         *     (`diff.Summarize`) and when it was made. Page with `before` (the `next_before` of the previous page).
         */
        get: operations["listTimelineVersions"];
        put?: never;
        /**
         * Edit the timeline with ops (a manual tweak, or the editor agent's tool)
         * @description Applies a batch of edit ops (`packages/schemas/timeline/ops.v1.schema.json`, 1 to 100) to version `base_version`,
         *     atomically, and stores the result as the next version, with its diff and summary; the author is the caller (`user`).
         *     `base_version` is the version the client was looking at, and 0 for a project that has no timeline yet (the first
         *     batch builds version 1 from an empty timeline of the project's format and size). If it is not the current version
         *     the answer is `409 version_conflict` and nothing is stored: fetch the project again and decide.
         *     The ops may only use the project's own footage that is `ready`: an op that names any other asset (another project's,
         *     another workspace's, one that does not exist) fails with an `unknown_asset` issue. A batch the engine refuses, or that
         *     leaves the timeline invalid, is `422 invalid_ops` with the issues (each names the op); a batch that changes nothing is
         *     `422 no_change`. Only owners and editors may edit.
         */
        post: operations["applyTimelineOps"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/projects/{id}/timelines/{version}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
                /** @description The version number, from 1. */
                version: number;
            };
            cookie?: never;
        };
        /**
         * One version of a project's timeline, with its spec and validation issues
         * @description The full spec (`packages/schemas/timeline/v1.schema.json`), the ops and the diff that made it, and the issues of
         *     validating the spec now against the project's footage (a clip whose asset was deleted since is reported as
         *     `unknown_asset`; the stored version itself never changes).
         */
        get: operations["getTimelineVersion"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/projects/{id}/edit": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Start an editor run on the project
         * @description Starts the editor agent on the project: it looks at the project's footage, builds the timeline with edit ops (each one a
         *     new timeline version, authored by the run) and ends with a message for the creator. The call only creates the run and
         *     returns it (`202`, status `queued`); follow it with `GET /v1/runs/{id}`. Only owners and editors may start a run.
         *     A project has at most one unfinished editor run (`409 run_in_progress`), and needs at least one ready video clip
         *     (`422 assets_not_usable`; an audio file is not a clip to cut).
         *     `instructions` is the creator's extra instruction for this run (text, at most 2,000 characters; the model treats it as data
         *     that steers the content of the video, nothing else). `profile` picks the model profile of the run; the default is the
         *     server's. `vibe` is the energy of the edit (`calm`, `lively` or `hype`, default `lively`): see `EditVibe`. An audio file in
         *     the project's footage is a song the edit can be cut to (D-34): the export then has the song and no clip sound under it.
         *
         *     **Credits.** The run costs credits, set aside (held) in the same step that creates the run: the server decides from the
         *     project whether it is the first edit (6 credits: the project has no charged edit yet) or a revision (2 credits), and the
         *     price comes from the server's price list, never from the request. A workspace with fewer credits available than that gets
         *     `402 credits_exhausted` and no run is created (a workspace with no active plan gets `402 plan_required`, as everywhere). A workspace
         *     on a BYOK plan (studio) pays for the model with its own Anthropic key: until it has one the start is refused with
         *     `402 byok_key_required` and no run is created.
         *
         *     A run that is cancelled before it made anything, finds nothing to change, or fails for a reason of the creator's own (it used
         *     its whole budget without anything usable, the model declined the content, the project no longer fits the model) is released:
         *     no credits are charged, but it has used model time. A workspace may have 5 such runs per UTC day on every plan, counting the runs
         *     it has in progress and the new one; the next start is refused with `429 too_many_uncharged_runs` until the next UTC day. Runs
         *     that cost nothing, runs that are charged, and failures on our side do not count. (A workspace that is out of credits is told
         *     that first, with `402 credits_exhausted`.)
         *     The credits are charged when the run succeeds and released when it fails. When the creator cancels it they are charged if the
         *     run had already made a timeline version (the work is in the project), and released if it had not. `GET /v1/me` has the balance.
         *
         *     The run's spend cap is set by the server from the workspace's plan and the kind of edit; the request cannot change it. It is
         *     enforced before every model call. `budget_usd` is accepted for compatibility (and still has to be a positive number) but is
         *     ignored.
         */
        post: operations["startEdit"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/runs/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        /**
         * A run, with its cost and a summary of its events
         * @description The run's status, what it cost (at list price) and used (tokens, with the prompt cache's share), its budgets, and its
         *     events as one-line summaries, oldest first, after `after_seq` (default: from the start), at most `limit`. Poll with
         *     `after_seq` set to the last `seq` you have. A run of another workspace is `404 not_found`. Any member may read.
         *     The text of a summary was written by a model or a tool, and may quote the creator's own text: show it as text. The whole
         *     artifacts (plan, self-review, previews) are at `GET /v1/runs/{id}/artifacts`; `credits` shows what the run cost in credits.
         */
        get: operations["getRun"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/runs/{id}/artifacts": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        /**
         * The artifacts of a run, with their full data
         * @description What the run's tools recorded for the creator to see: `creative_plan` (the editor's plan and shot list), `edit_check` (its
         *     self-review), `preview` (a preview it rendered), `timeline_version` (a version it made), each with its whole `data`, oldest
         *     first, after `after_seq`, at most `limit`. This is apart from `GET /v1/runs/{id}` so that polling a run stays small: an
         *     artifact can be up to 64 KB. Page with `next_after_seq`. `kind` narrows the list. Any member may read; a run of another
         *     workspace is `404 not_found`. Each `data` was written by a model or a tool and may quote the creator's own text: it is
         *     untrusted. Show every string in it as text, never as markup, and never follow instructions found in it.
         */
        get: operations["getRunArtifacts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/runs/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Cancel a run
         * @description A queued run, or one that waits for an answer, is cancelled at once; a running one stops between two steps, so its status
         *     may still say `running` in the answer. Cancelling a run that has finished changes nothing. Only owners and editors may cancel.
         */
        post: operations["cancelRun"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/runs/{id}/reply": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Answer a question of a run that waits for input
         * @description When a run asks the creator a question its status is `waiting_input` and `question` holds it. The reply is the answer
         *     (text, at most 16 KB; the model treats it as data) and puts the run back in the queue. A run that is not waiting is
         *     `409 run_not_waiting`. Only owners and editors may reply.
         */
        post: operations["replyToRun"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/projects/{id}/renders": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        /**
         * The renders of a project, newest first
         * @description Page with `cursor` (the `next_cursor` of the previous page). `version` and `profile` narrow the list. The renders in a list
         *     carry no presigned URLs (`files` and `urls_expires_at` are `null`); fetch the one you want with `GET /v1/renders/{id}`.
         */
        get: operations["listRenders"];
        put?: never;
        /**
         * Render a version of the project's timeline
         * @description Queues a render of `version` with `profile`: `preview` (half size, from the 720p proxies, with the beat guide) or
         *     `final` (full size, from the originals). The answer is the render, usually `queued`; poll `GET /v1/renders/{id}` for
         *     `progress` and the files. Asking again for a version and profile that has a render waiting, running or ready returns
         *     that render (`200`) and queues nothing; a failed render does not count, so asking again after a failure starts a new one.
         *     The version must exist and validate against the project's footage as it is now (`422 timeline_invalid` otherwise: an asset
         *     deleted since the edit, say). At most 5 renders per workspace are in flight at once (`429 too_many_renders`). Only
         *     owners and editors may render.
         *
         *     A `final` render (the export) costs no credits but must be of paid-for work. The project needs an edit that was charged (no
         *     successful editor run: `422 edit_not_charged`), and the version must be covered: the last editor-made version at or before
         *     it must come from a run that was charged. A person's own edits on top of a charged version are free to export; a version
         *     made by a run that failed or was released, and edits made on top of it, are refused with `422 version_not_charged` until a
         *     later editor run is charged. A queued final render is recorded on the credit ledger as the `export.final` action at 0
         *     credits. Previews of any version are never refused.
         */
        post: operations["createRender"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/renders/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The render's TypeID. */
                id: components["parameters"]["RenderIdPath"];
            };
            cookie?: never;
        };
        /**
         * One render, with its progress and, once ready, presigned URLs for its files
         * @description A render of another workspace, of a deleted project, or one that does not exist is `404 not_found`. While `status` is
         *     `queued` or `rendering`, `progress` (0 to 100) and `stage` say how far it is. Once `ready`, `files` holds a presigned GET
         *     URL for the MP4, the contact sheet (a JPEG) and the time map (JSON), each valid for 30 minutes (`urls_expires_at`);
         *     fetch the render again for fresh ones. `failure_reason` is a code that is safe to show when `status` is `failed`.
         */
        get: operations["getRender"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/annotations": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * The comments on one target, or on a project
         * @description Either `target_type` with `target_id` (the comments on one clip, render or version) or `project_id` (every comment on the renders
         *     and versions of the project): not both, and one of the two is required. Replies come in the same list, with `parent_id` set. Oldest
         *     first; page with `cursor` (the `next_cursor` of the previous page). Every role may read.
         */
        get: operations["listAnnotations"];
        put?: never;
        /**
         * Comment on a clip, a render or a version of a timeline
         * @description Adds a comment on a range of `target_id`'s time. `target_type` says what that is: `asset` (a clip of the library, `ast_`), `render`
         *     (an output, `ren_`) or `timeline_version` (a stored version, `tl_`, for a version that has no render yet). A comment on a render
         *     or a version carries the project and the version it was made on (`project_id`, `timeline_version`), which the server reads from the
         *     target; `project_id` in the request is optional, and when it is there it must be the target's project (`404` otherwise). A
         *     point comment has `end_ms` equal to `start_ms`; a range may reach at most 34 ms (one frame) past the end of the clip or render
         *     when its length is known. `pin` is a point or a box on the frame, normalised to 0..1 from the top left.
         *     `kind` is `comment` (the default), `instruction`, or one of the constraints `include`, `exclude` and `prefer`. With `parent_id` the
         *     comment is a reply to a comment: it takes the target and range of that comment, has kind `comment` and no pin, and a reply to a reply is
         *     refused. `body` is plain text of at most 2,000 characters (a bare marker may have none, except a `comment` or an `instruction`).
         *     A target or a parent of another workspace is `404 not_found`, the same as one that does not exist. Only owners and editors may
         *     comment.
         */
        post: operations["createAnnotation"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/v1/annotations/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The annotation's TypeID. */
                id: components["parameters"]["AnnotationIdPath"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Delete a comment
         * @description Soft-deletes the comment and its replies. Only the author may delete a comment (`403 insufficient_role` for anyone else, the editor's replies included).
         */
        delete: operations["deleteAnnotation"];
        options?: never;
        head?: never;
        /**
         * Edit, resolve or reopen a comment
         * @description Changes what the request names and leaves the rest. The author of a comment may change anything; another owner or editor may only
         *     change `status` (to resolve or reopen it), and the editor's replies are never edited (`403 insufficient_role`). `status` takes `open`
         *     or `resolved`: `applied` is set by the editor agent when a run has dealt with the comment, and a person reopens it with `open`.
         *     Editing the text, kind, range or pin of an applied comment reopens it. `remove_pin` takes the pin off. The range of a reply
         *     cannot be changed (it follows its comment). Editing a comment moves the range of its replies with it. Sending only `start_ms`
         *     shifts the range and keeps its length (a moment stays a moment); sending only `end_ms` changes the length; send both to set the
         *     range. A range may reach at most 34 ms (one frame) past the end of the clip or render.
         */
        patch: operations["updateAnnotation"];
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
        ErrorCode: "invalid_request" | "unauthenticated" | "invalid_token" | "plan_required" | "credits_exhausted" | "no_active_workspace" | "user_deleted" | "workspace_deleted" | "not_a_member" | "origin_not_allowed" | "invalid_signature" | "invalid_event" | "payload_too_large" | "internal_error" | "identity_unavailable" | "auth_not_configured" | "queue_not_migrated" | "webhook_not_configured" | "insufficient_role" | "not_found" | "upload_not_active" | "upload_expired" | "file_too_large" | "unsupported_content_type" | "parts_mismatch" | "size_mismatch" | "too_many_uploads" | "storage_unavailable" | "storage_not_configured" | "assets_not_usable" | "version_conflict" | "invalid_ops" | "no_change" | "timeline_invalid" | "too_many_renders" | "run_in_progress" | "run_not_waiting" | "model_not_configured" | "edit_not_charged" | "too_many_uncharged_runs" | "byok_key_required" | "version_not_charged";
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
            credits?: components["schemas"]["Credits"];
        };
        /** @description Credits that are not the period's allowance (bought or given) and when they expire. */
        CreditTopUp: {
            /** @description What is left of this top-up. */
            credits: number;
            /** Format: date-time */
            expires_at: string;
        };
        /**
         * @description The workspace's credits (1 video = 10 credits). Credits in use by a run that has not finished are already taken out of
         *     `available`. Allowance that is not used by the end of its period expires; top-ups last 12 months. The allowance is used first,
         *     then the top-ups, earliest expiry first.
         */
        Credits: {
            /** @description What a new action can use now, allowance and top-ups together. */
            available: number;
            /** @description The size of the current period's allowance; 0 when the workspace has none. */
            allowance: number;
            /**
             * Format: date-time
             * @description When the current allowance expires and the next is granted. `null` when there is none, or it does not end (a trial's).
             */
            period_end: string | null;
            /** @description At least 80 % of the allowance is used (`available` is at most a fifth of `allowance`). Always false without an allowance. */
            low: boolean;
            top_ups: components["schemas"]["CreditTopUp"][];
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
        /**
         * @description Where the title came from: `filename` (the file name without its extension, what the upload made), `quicklook`
         *     (written by the quick look) or `user` (edited with `updateAsset`; never overwritten).
         * @enum {string}
         */
        TitleSource: "filename" | "quicklook" | "user";
        /**
         * @description `pending` (the clip is ready and its quick look has not finished), `succeeded` (title, description and tags were
         *     written), `failed` (the clip is usable but has no description or tags; the title is still the file name) or
         *     `skipped` (nothing is owed: the asset failed its ingest, or the job could not run for a day).
         * @enum {string}
         */
        QuickLookStatus: "pending" | "succeeded" | "failed" | "skipped";
        UpdateAssetRequest: {
            /** @description The new title. Cleaned by the server; 1 to 255 characters must remain. */
            title: string;
        };
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
            /** @description Editable with `updateAsset`. Starts as the file name without its extension; the quick look replaces it unless it was edited. */
            title: string;
            /** @description Present on every asset the server returns (optional in the schema only so older clients and fixtures still type-check). */
            title_source?: components["schemas"]["TitleSource"];
            /** @description A one-line description written by the quick look (at most 160 characters). Empty until then. */
            description: string;
            /** @description At most 8 lowercase tags of one to three words, written by the quick look. Empty until then. */
            tags: string[];
            /** @description The state of the quick look (the title, description and tags a video gets right after its ingest). Absent for an asset it does not apply to (audio, images, and videos from before it existed). */
            quick_look_status?: components["schemas"]["QuickLookStatus"];
            /**
             * @description Why the asset is `failed`, as a code that is safe to show. The upload failed: `upload_expired`, `size_mismatch`,
             *     `upload_aborted`. The ingest could not process the file: `unsupported_format` (not a container we accept),
             *     `corrupt_media` (damaged or cut off), `no_video_stream`, `no_audio_stream`, `undecodable_video`,
             *     `invalid_duration`, `too_long` (over 30 minutes), `unsupported_resolution`, `kind_mismatch`; or, our fault, the
             *     ingest failed on every retry: `processing_failed`, `processing_timeout` (it ran out of time) or
             *     `processing_interrupted` (the worker was stopped on the last attempt). A failed asset has no derivatives.
             *     Null otherwise.
             */
            failure_reason: string | null;
            /** @description How far the ingest is while `status` is `processing`; 100 when `ready`, 0 before it starts. */
            ingest_progress: number;
            /** @description What the ingest is doing while `status` is `processing` (`starting`, `downloading`, `probing`, `proxy`, `derivatives`, `uploading`). Null otherwise. */
            ingest_stage: string | null;
            transcript_status: components["schemas"]["TranscriptStatus"];
            /**
             * @description Why the transcript is `failed`, as a code that is safe to show: `spend_cap` (the workspace's daily spend cap could not
             *     cover the transcription), `minutes_cap` (the workspace had its daily minutes of transcription already),
             *     `unusable_audio`, `gave_up` (every attempt failed). Null otherwise. Absent on older responses.
             */
            transcript_failure_reason?: string | null;
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
        /**
         * @description Where the transcript of the asset stands. `none`: there is none and none is coming (a picture, a clip without an audio
         *     track, or the ingest has not finished). `pending`: the transcription job is queued or running. `no_speech`: the clip was
         *     checked and has no speech. `ready`: the words are available at `GET /v1/assets/{id}/transcript`. `failed`: the job gave up.
         * @enum {string}
         */
        TranscriptStatus: "none" | "pending" | "no_speech" | "ready" | "failed";
        Transcript: {
            asset_id: components["schemas"]["AssetId"];
            status: components["schemas"]["TranscriptStatus"];
            /** @description False when the voice-activity check found no speech; `words` is then empty. */
            speech: boolean;
            /** @description The spoken language as an ISO 639-1 code (`en`, `nl`); empty without speech. */
            language: string;
            words: components["schemas"]["TranscriptWord"][];
            structure: components["schemas"]["SpeechStructure"];
            /** @description Who transcribed it (`whispercpp`, `workersai`). The words have the same shape whichever it was. */
            provider: string;
            model: string;
            /**
             * Format: int64
             * @description The length of the audio that was checked.
             */
            audio_ms: number;
            /**
             * Format: int64
             * @description How much speech the voice-activity check found.
             */
            speech_ms: number;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
        };
        TranscriptWord: {
            /** @description The word as written, with its punctuation (`Hello,`). */
            text: string;
            /**
             * Format: int64
             * @description Milliseconds from the start of the asset. Placed on the audio itself, not only on the model's estimate.
             */
            start_ms: number;
            /** Format: int64 */
            end_ms: number;
            /**
             * Format: double
             * @description The model's probability for the word, 0 to 1; 0 when the provider does not report one.
             */
            confidence: number;
        };
        /**
         * @description Derived from the words (so it is the same for every provider): where sentences end, which words are fillers, where the
         *     speaker pauses. Word indexes are positions in `words`.
         */
        SpeechStructure: {
            sentences: components["schemas"]["SpeechSentence"][];
            fillers: components["schemas"]["SpeechFiller"][];
            /** @description Silences between two words that last longer than 400 ms. */
            pauses: components["schemas"]["SpeechPause"][];
        };
        SpeechSentence: {
            first_word: number;
            /** @description Included. */
            last_word: number;
            /** Format: int64 */
            start_ms: number;
            /** Format: int64 */
            end_ms: number;
            text: string;
        };
        SpeechFiller: {
            first_word: number;
            /** @description Included; a phrase such as "you know" spans two words. */
            last_word: number;
            /** Format: int64 */
            start_ms: number;
            /** Format: int64 */
            end_ms: number;
            text: string;
            /**
             * @description `hesitation` is a sound without meaning (um, uh, eh); `discourse` is a word or phrase that is empty here but a word elsewhere (like, you know, zeg maar).
             * @enum {string}
             */
            kind: "hesitation" | "discourse";
            /**
             * @description `strong` fillers are nearly always safe to cut; `weak` ones depend on context.
             * @enum {string}
             */
            strength: "strong" | "weak";
        };
        SpeechPause: {
            /** @description The index of the word before the pause. */
            after_word: number;
            /** Format: int64 */
            start_ms: number;
            /** Format: int64 */
            end_ms: number;
            /** Format: int64 */
            duration_ms: number;
            /** @description True when the pause falls between two sentences, false when the speaker stopped mid-sentence. */
            sentence_break: boolean;
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
            /** @description `sprite.vtt` as stored: a WebVTT thumbnails track whose cues name the sheet relatively (`sprite.jpg#xywh=x,y,w,h`). Do not give this URL to a player; `GET /v1/assets/{id}/sprite.vtt` returns the same track with every cue pointing at the sprite URL. */
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
        /**
         * @description The output format (D-25). It fixes the size of the rendered video (1080x1920, 1080x1080, 1080x1350, 1920x1080).
         * @enum {string}
         */
        ProjectFormat: "9:16" | "1:1" | "4:5" | "16:9";
        /** @enum {string} */
        ProjectStatus: "draft" | "editing" | "review" | "approved" | "exported";
        /** @enum {string} */
        AuthorKind: "user" | "agent";
        CreateProjectRequest: {
            /** @description Defaults to "Untitled project". */
            title?: string;
            format: components["schemas"]["ProjectFormat"];
            /** @description The footage the editor may use, in order. Each must be a `ready` video or audio asset of this workspace. */
            asset_ids: components["schemas"]["AssetId"][];
            /** @description The creator's instructions, free text. */
            brief?: string;
        };
        ProjectAsset: {
            asset_id: components["schemas"]["AssetId"];
            position: number;
            title: string;
            kind: components["schemas"]["AssetKind"];
            status: components["schemas"]["AssetStatus"];
            /** Format: int64 */
            duration_ms: number | null;
            /** @description False when the asset was deleted or is no longer `ready`; the editor cannot use it then. */
            available: boolean;
        };
        TimelineVersionSummary: {
            id: components["schemas"]["TimelineId"];
            version: number;
            /** @description The version this one was made from. Null for version 1. */
            parent_version: number | null;
            author_kind: components["schemas"]["AuthorKind"];
            /**
             * @description For `user`, the TypeID of the user (`usr_...`). For `agent`, the TypeID of the editor run (`run_...`) that made the
             *     version; `GET /v1/runs/{id}` shows the run. (The key that makes a repeated tool call idempotent is stored apart from it.)
             */
            author_id: string;
            /** @description One line per change, for the version list and the run feed. */
            summary: string[];
            /** Format: date-time */
            created_at: string;
        };
        ProjectSummary: {
            id: components["schemas"]["ProjectId"];
            title: string;
            format: components["schemas"]["ProjectFormat"];
            status: components["schemas"]["ProjectStatus"];
            asset_count: number;
            /** @description Null until the first edit. */
            current_timeline_version: number | null;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
        };
        Project: {
            id: components["schemas"]["ProjectId"];
            title: string;
            format: components["schemas"]["ProjectFormat"];
            brief: string;
            status: components["schemas"]["ProjectStatus"];
            /** @description The footage that is not deleted (`assets` also lists an asset deleted since, with `available` false). The list shows the same count. */
            asset_count: number;
            /** @description Null until the first edit creates version 1. */
            current_timeline_version: number | null;
            current_version: components["schemas"]["TimelineVersionSummary"] | null;
            /** @description The footage, in the order the creator chose. */
            assets: components["schemas"]["ProjectAsset"][];
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
        };
        ListProjectsResponse: {
            items: components["schemas"]["ProjectSummary"][];
            /** @description Pass it as `cursor` for the next page. Null on the last page. */
            next_cursor: string | null;
        };
        ListTimelineVersionsResponse: {
            items: components["schemas"]["TimelineVersionSummary"][];
            /** @description Pass it as `before` for the next page. Null on the last page. */
            next_before: number | null;
        };
        /** @description One finding of validating a timeline spec (docs/design/timeline-v1.md, section 3). */
        TimelineIssue: {
            /** @description `schema`, `duplicate_id`, `gap`, `overlap`, `source_out_of_range`, `unknown_asset`, ... */
            code: string;
            /** @description A JSON Pointer into the spec. Empty for the whole document. */
            path: string;
            message: string;
            /** @enum {string} */
            severity: "error" | "warning";
        };
        /** @description One problem with an edit batch. It names the op that caused it when that is known. */
        OpIssue: {
            /** @description The index in the batch of the op that caused the issue, -1 when no op in particular did. */
            op_index: number;
            /** @description The name of that op. */
            op?: string;
            /** @description An op code (`invalid_op`, `unknown_op`, `unknown_item`, `bad_position`, `id_taken`, ...) or a timeline code (`unknown_asset`, `shot_too_short`, ...). */
            code: string;
            /** @description A JSON Pointer into the op, for a problem with the op itself. */
            field?: string;
            /** @description A JSON Pointer into the resulting spec, for a problem the validator found. */
            path?: string;
            message: string;
            /** @enum {string} */
            severity: "error" | "warning";
        };
        /** @description An item an `add_*` or `set_sound` op made, with the id it got. */
        CreatedItem: {
            op_index: number;
            /** @enum {string} */
            kind: "clip" | "text" | "sound";
            id: string;
        };
        TimelineVersion: components["schemas"]["TimelineVersionSummary"] & {
            /** @description The timeline, as defined by `packages/schemas/timeline/v1.schema.json`. */
            spec: {
                [key: string]: unknown;
            };
            /** @description The batch that made this version from its parent. Empty for version 1 when it did not come from ops. */
            ops: {
                [key: string]: unknown;
            }[];
            /** @description The RFC 6902 JSON Patch from the parent's spec to this one's. */
            diff: {
                [key: string]: unknown;
            }[];
            /** @description What validating the spec against the project's footage finds now. Empty for a clean version. */
            issues: components["schemas"]["TimelineIssue"][];
        };
        ApplyTimelineOpsRequest: {
            /** @description The version the ops are meant for; 0 when the project has no timeline yet. */
            base_version: number;
            /** @description The edit ops, each an object with an `op` field (see `ops.v1.schema.json`). They are checked strictly. */
            ops: {
                [key: string]: unknown;
            }[];
        };
        ApplyTimelineOpsResponse: {
            version: components["schemas"]["TimelineVersion"];
            created: components["schemas"]["CreatedItem"][];
            warnings: components["schemas"]["OpIssue"][];
        };
        /** @description The error body of a batch that was refused. `issues` says why, op by op. */
        OpsRejected: {
            error: components["schemas"]["ErrorDetail"];
            issues: components["schemas"]["OpIssue"][];
        };
        /**
         * @description A model routing profile (config/routing): `dev-cheap` (Haiku, cents per run), `dev-mid` or `v1-start` (the editor on Opus).
         * @enum {string}
         */
        RunProfile: "dev-cheap" | "dev-mid" | "v1-start";
        StartEditRequest: {
            /** @description Extra instructions for this run, in the creator's words. Data for the model, not commands. */
            instructions?: string;
            profile?: components["schemas"]["RunProfile"];
            vibe?: components["schemas"]["EditVibe"];
            /** @description Ignored. The run's spend cap is set by the server from the plan (it used to be the caller's, up to $5). */
            budget_usd?: number;
        };
        /**
         * @description The energy of the edit (P6-06). The creator's style profile still decides the colours, the fonts, the title's look and place and
         *     the rough shot length; the vibe decides how much moves: `calm` stays close to the creator's own restraint (hard cuts, a quiet
         *     title), `lively` (the default) puts transitions on the strong beats and the drop, gives every text an entrance and an exit and
         *     keeps the camera moving, `hype` does that on more beats with faster shots.
         * @default lively
         * @enum {string}
         */
        EditVibe: "calm" | "lively" | "hype";
        ReplyRequest: {
            /** @description The answer to the run's question. */
            text: string;
        };
        /** @enum {string} */
        RunStatus: "queued" | "running" | "waiting_input" | "succeeded" | "failed" | "cancelled";
        /** @enum {string} */
        RunEventType: "status" | "assistant_message" | "tool_call" | "tool_result" | "error" | "artifact";
        /** @description One step of a run, as a line of text. */
        RunEvent: {
            /** @description The event's place in the run, from 1. */
            seq: number;
            type: components["schemas"]["RunEventType"];
            /** @description One line, at most 400 bytes. Text a model or a tool wrote; show it as text. */
            text: string;
            /** @description The tool of a `tool_call` or `tool_result`. */
            tool?: string | null;
            /** @description A failed tool call, or an `error` event. */
            is_error?: boolean;
            /**
             * @description The model's tool call id. A `tool_call` and its `tool_result` have the same one, and an `artifact` has the id of the call
             *     that made it, so pair them by this and not by order. Absent on the other events. An opaque string.
             */
            call_id?: string | null;
            /** @description The kind of an `artifact` event, such as `creative_plan` or `edit_check`; absent on the other events. */
            kind?: string | null;
            /** Format: date-time */
            created_at: string;
        };
        /**
         * @description Something a run's tool recorded. `data` was written by a model or a tool and may quote the creator's own text: untrusted,
         *     show its strings as text only.
         */
        RunArtifact: {
            /** @description The artifact event's place in the run (the `seq` of its `RunEvent`). */
            seq: number;
            /**
             * @description What it is: `creative_plan`, `edit_check`, `preview` or `timeline_version` for the editor (see agent-runtime-v1 for each
             *     one's fields).
             */
            kind: string;
            /** @description The tool call that made it (the `call_id` of its `tool_call` and `tool_result` events). */
            call_id: string | null;
            /** @description The artifact itself, at most 64 KB; `null` for an artifact event that has none. Its shape depends on `kind`. Untrusted text. */
            data: {
                [key: string]: unknown;
            } | null;
            /** Format: date-time */
            created_at: string;
        };
        RunArtifacts: {
            artifacts: components["schemas"]["RunArtifact"][];
            /** @description Pass as `after_seq` for the next page; `null` when this is the last page so far (poll again for more). */
            next_after_seq: number | null;
        };
        /**
         * @description What the run costs in credits, from the credit ledger. A run that was released (failed, cancelled before it made anything, or
         *     found nothing to change) is not charged and has no `credits` object at all.
         */
        RunCredits: {
            /**
             * @description `held`: set aside while the run works. `charged`: taken. `refunded`: taken, then given back.
             * @enum {string}
             */
            state: "held" | "charged" | "refunded";
            /** @enum {string} */
            action: "edit.first" | "edit.revision";
            /** @description How many credits (for `refunded`, how many were given back, which is less than the charge when part of it came from credits that had expired). */
            credits: number;
        };
        /**
         * @description What the run's model calls used. `cache_hit_rate` is `cache_read` over all prompt tokens (`input` + `cache_write` +
         *     `cache_read`), `null` before the first call.
         */
        RunTokens: {
            /** Format: int64 */
            input: number;
            /** Format: int64 */
            cache_write: number;
            /** Format: int64 */
            cache_read: number;
            /** Format: int64 */
            output: number;
            cache_hit_rate: number | null;
        };
        Run: {
            id: components["schemas"]["RunId"];
            /** @description The agent, such as `editor`. */
            agent: string;
            project_id?: components["schemas"]["ProjectId"];
            status: components["schemas"]["RunStatus"];
            /** @description Why a `failed` run failed, such as `budget`, `model_error`, `daily_cap` or `internal_error`. `null` otherwise. */
            failure_reason?: string | null;
            /** @description The question of a run that is `waiting_input`; answer it with `POST /v1/runs/{id}/reply`. */
            question?: string | null;
            /** @description The message for the creator that a finished run ended with (for a run that stopped early, why it did). */
            summary?: string | null;
            /** @description The timeline version the run finished on, for a run that finished the edit. */
            timeline_version?: number | null;
            routing_profile: string;
            model: string;
            effort: string;
            /** @description Who pays for the run's model calls (`platform`, `workspace_key` or `test`). */
            billed_to: string;
            /**
             * Format: int64
             * @description What the run's model calls cost at list price, in millionths of a dollar.
             */
            cost_micros: number;
            /**
             * Format: int64
             * @description The run's spend cap, in millionths of a dollar.
             */
            budget_micros: number;
            tokens: components["schemas"]["RunTokens"];
            tool_calls: number;
            budget_tool_calls: number;
            /**
             * Format: int64
             * @description How long the run has been running (waiting for an answer does not count).
             */
            active_ms: number;
            /** Format: int64 */
            budget_wall_clock_ms: number;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            started_at?: string | null;
            /** Format: date-time */
            finished_at?: string | null;
            /**
             * @description What the run costs in credits; only on `GET /v1/runs/{id}`, and absent for a run that took none (a released run, a
             *     run without credits) and for the answers of start, cancel and reply.
             */
            credits?: components["schemas"]["RunCredits"] | null;
            events: components["schemas"]["RunEvent"][];
            /** @description The sequence number of the run's last event (not only of the ones returned), 0 before the first. */
            last_seq: number;
        };
        /**
         * @description `preview`: half the format's size, from the 720p proxies, x264 veryfast, with the beat guide. `final`: the format's size, from the originals, x264 medium, two-pass loudness.
         * @enum {string}
         */
        RenderProfile: "preview" | "final";
        /** @enum {string} */
        RenderStatus: "queued" | "rendering" | "ready" | "failed";
        CreateRenderRequest: {
            /** @description The timeline version to render. */
            version: number;
            profile: components["schemas"]["RenderProfile"];
        };
        RenderFile: {
            /** @description A presigned GET URL. Treat it as a secret until it expires. */
            url: string;
            content_type: string;
            /** Format: int64 */
            size_bytes: number;
        };
        ContactSheetTile: {
            /** @description The tile's place in the sheet, row by row, from 0. */
            index: number;
            /**
             * Format: int64
             * @description The output time of the frame shown, the timestamp drawn on the tile.
             */
            out_ms: number;
            /**
             * Format: int64
             * @description The output frame number.
             */
            frame: number;
            /** @description The main-track item that owns that time (the shot). */
            item_id?: string;
            /**
             * @description `shot`: 100 ms after the shot starts (the first shot starts at 0, the others at each cut). `last`: the last frame of the video.
             * @enum {string}
             */
            kind: "shot" | "last";
        };
        /**
         * @description The layout of the contact sheet: one tile per shot (the frame 100 ms after the cut that starts it) and the last frame,
         *     tiled `columns` across, each with its output timestamp (`m:ss.mmm`) drawn on it.
         */
        ContactSheet: {
            columns: number;
            tile_width: number;
            tile_height: number;
            tiles: components["schemas"]["ContactSheetTile"][];
        };
        RenderFiles: {
            mp4: components["schemas"]["RenderFile"];
            contact_sheet: components["schemas"]["RenderFile"];
            /** @description A JSON array of `{out_start_ms, out_end_ms, item_id, asset_id?, source_in_ms?, speed?}`: main items carry the asset, the source in point and the speed, text items the first three fields only. An output time `t` inside an item is the source time `source_in_ms + (t - out_start_ms) * speed`. */
            time_map: components["schemas"]["RenderFile"];
        };
        /** @description A sound the timeline asks for that is never mixed in (decision D-03); the creator adds it in the app. */
        SoundCue: {
            item_id: string;
            /** @description The trend's TypeID. */
            trend_id: string;
            /** Format: int64 */
            at_ms: number;
            /** Format: int64 */
            source_offset_ms: number;
            /** Format: int64 */
            duration_ms: number;
            gain_db: number;
        };
        RequestedBy: {
            kind: components["schemas"]["AuthorKind"];
            /** @description The user's TypeID, or the editor run's id. */
            id: string;
        };
        Render: {
            id: components["schemas"]["RenderId"];
            project_id: components["schemas"]["ProjectId"];
            timeline_version: number;
            profile: components["schemas"]["RenderProfile"];
            status: components["schemas"]["RenderStatus"];
            progress: number;
            /** @description What a running render is doing: `staging` (fetching the footage), `measuring` (the loudness pass of the final profile), `rendering`, `contact_sheet`, `uploading`. Null otherwise. */
            stage: string | null;
            /**
             * Format: int64
             * @description The length of the video. Null until ready.
             */
            duration_ms: number | null;
            width: number | null;
            height: number | null;
            /**
             * Format: int64
             * @description The size of the MP4. Null until ready.
             */
            size_bytes: number | null;
            contact_sheet: components["schemas"]["ContactSheet"] | null;
            sound_cues: components["schemas"]["SoundCue"][];
            /** @description Null until the render is ready, and in lists (fetch the render itself for the URLs). */
            files: components["schemas"]["RenderFiles"] | null;
            /** Format: date-time */
            urls_expires_at: string | null;
            /** @description When `failed`: `invalid_timeline`, `source_unavailable` (an asset or its proxy is gone), `inputs_too_large`, `render_failed` (ffmpeg could not make the video), `project_deleted`, `job_lost` (the job that should have rendered it was gone), `timeout`, `interrupted` or `processing_failed`. */
            failure_reason: string | null;
            requested_by: components["schemas"]["RequestedBy"];
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            started_at: string | null;
            /** Format: date-time */
            finished_at: string | null;
        };
        ListRendersResponse: {
            items: components["schemas"]["Render"][];
            /** @description Pass it as `cursor` for the next page. Null on the last page. */
            next_cursor: string | null;
        };
        /**
         * @description What a comment is about, and what `target_id` is a TypeID of.
         * @enum {string}
         */
        AnnotationTargetType: "asset" | "render" | "timeline_version";
        /**
         * @description `comment` (a note for the creator or the editor), `instruction` (a note addressed to the editor agent), or a constraint on the range:
         *     `include` (keep it), `exclude` (leave it out), `prefer` (favour it).
         * @enum {string}
         */
        AnnotationKind: "comment" | "include" | "exclude" | "prefer" | "instruction";
        /**
         * @description `open`; `applied` (an editor run dealt with it: `applied_in_run_id`, and `addressed_in_version` when that run made a version); or
         *     `resolved` (closed by hand).
         * @enum {string}
         */
        AnnotationStatus: "open" | "applied" | "resolved";
        /**
         * @description A spot on the frame, normalised to 0..1 from the top left: a point (`x`, `y`) or a box (`x`, `y` and its size `w`, `h`, which
         *     come together and keep the box inside the frame).
         */
        AnnotationPin: {
            /** Format: double */
            x: number;
            /** Format: double */
            y: number;
            /** Format: double */
            w?: number;
            /** Format: double */
            h?: number;
        };
        AnnotationAuthor: {
            kind: components["schemas"]["AuthorKind"];
            /** @description The user's TypeID, or the editor run's id. */
            id: string;
        };
        Annotation: {
            id: components["schemas"]["AnnotationId"];
            target_type: components["schemas"]["AnnotationTargetType"];
            /** @description The TypeID of the clip, render or version. */
            target_id: string;
            /** @description The project of a comment on a render or a version. Null for a clip. */
            project_id: components["schemas"]["ProjectId"] | null;
            /** @description The version a comment on a render or a version was made on (of a render, the version it was made from). Null for a clip. */
            timeline_version: number | null;
            /** Format: int64 */
            start_ms: number;
            /**
             * Format: int64
             * @description Equal to `start_ms` for a comment on a moment.
             */
            end_ms: number;
            pin: components["schemas"]["AnnotationPin"] | null;
            kind: components["schemas"]["AnnotationKind"];
            body: string;
            status: components["schemas"]["AnnotationStatus"];
            /** @description Set on a reply: the comment it answers. */
            parent_id: components["schemas"]["AnnotationId"] | null;
            author: components["schemas"]["AnnotationAuthor"];
            applied_in_run_id: components["schemas"]["RunId"] | null;
            /** @description The version the editor run that applied the comment made. Null while the comment is not applied, and when the run made none. */
            addressed_in_version: number | null;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
        };
        /**
         * @description `target_type`, `target_id` and `start_ms` are required, except in a reply (`parent_id`): a reply takes the target and the range of the
         *     comment it answers, and when it names them they must be the comment's own.
         */
        CreateAnnotationRequest: {
            target_type?: components["schemas"]["AnnotationTargetType"];
            /** @description The TypeID of the clip (`ast_`), the render (`ren_`) or the version (`tl_`). */
            target_id?: string;
            project_id?: components["schemas"]["ProjectId"];
            /** Format: int64 */
            start_ms?: number;
            /**
             * Format: int64
             * @description Defaults to `start_ms` (a comment on a moment).
             */
            end_ms?: number;
            pin?: components["schemas"]["AnnotationPin"];
            /** @description Defaults to `comment`. */
            kind?: components["schemas"]["AnnotationKind"];
            /** @description Plain text. Required (not empty) for a `comment` and an `instruction`. */
            body?: string;
            parent_id?: components["schemas"]["AnnotationId"];
        };
        /** @description Every field is optional; the ones that are absent stay as they are. At least one must be there. */
        UpdateAnnotationRequest: {
            body?: string;
            kind?: components["schemas"]["AnnotationKind"];
            /**
             * @description `open` (reopen) or `resolved`. A person cannot set `applied`.
             * @enum {string}
             */
            status?: "open" | "resolved";
            /** Format: int64 */
            start_ms?: number;
            /** Format: int64 */
            end_ms?: number;
            pin?: components["schemas"]["AnnotationPin"];
            /** @description Take the pin off. Not with `pin`. */
            remove_pin?: boolean;
        };
        ListAnnotationsResponse: {
            items: components["schemas"]["Annotation"][];
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
        /** @description The project's TypeID. */
        ProjectIdPath: components["schemas"]["ProjectId"];
        /** @description The annotation's TypeID. */
        AnnotationIdPath: components["schemas"]["AnnotationId"];
        /** @description The render's TypeID. */
        RenderIdPath: components["schemas"]["RenderId"];
        /** @description The run's TypeID. */
        RunIdPath: components["schemas"]["RunId"];
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
                /** @description Search words, at most 100 characters. At most the first 8 words are used. */
                q?: string;
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
    updateAsset: {
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
                "application/json": components["schemas"]["UpdateAssetRequest"];
            };
        };
        responses: {
            /** @description The asset as it is now. */
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
    getAssetSpriteVtt: {
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
            /** @description The track, `text/vtt`. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "text/vtt": string;
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
    getAssetTranscript: {
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
            /** @description The transcript. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Transcript"];
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
    listProjects: {
        parameters: {
            query?: {
                /** @description How many projects to return (default 50). */
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
            /** @description A page of projects. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListProjectsResponse"];
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
    createProject: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateProjectRequest"];
            };
        };
        responses: {
            /** @description The project. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Project"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description `assets_not_usable`: an asset is not `ready`, or is an image. The message names the assets. */
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
    getProject: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The project. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Project"];
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
    deleteProject: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
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
    listTimelineVersions: {
        parameters: {
            query?: {
                /** @description How many versions to return (default 100). */
                limit?: number;
                /** @description Only versions below this number. */
                before?: number;
            };
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A page of versions. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListTimelineVersionsResponse"];
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
    applyTimelineOps: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ApplyTimelineOpsRequest"];
            };
        };
        responses: {
            /** @description The new version. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ApplyTimelineOpsResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description `version_conflict`: `base_version` is not the project's current version. */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `invalid_ops` or `no_change`, with the issues the engine found. */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["OpsRejected"];
                };
            };
            500: components["responses"]["InternalError"];
            503: components["responses"]["Unavailable"];
        };
    };
    getTimelineVersion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
                /** @description The version number, from 1. */
                version: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The version. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TimelineVersion"];
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
    startEdit: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "application/json": components["schemas"]["StartEditRequest"];
            };
        };
        responses: {
            /** @description The run, queued. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Run"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description `run_in_progress`: the project's editor run has not finished. */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `assets_not_usable`: the project has no ready video clip to edit. */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `too_many_uncharged_runs`: the workspace has had today's editor runs that spent money and ended without a charge. */
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
    getRun: {
        parameters: {
            query?: {
                /** @description Only events after this sequence number. */
                after_seq?: number;
                /** @description How many events to return (default 200). */
                limit?: number;
            };
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The run. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Run"];
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
    getRunArtifacts: {
        parameters: {
            query?: {
                /** @description Only artifacts after this sequence number (the `seq` of a `RunEvent`). */
                after_seq?: number;
                /** @description Only artifacts of this kind, such as `creative_plan`. 1 to 64 lowercase letters and underscores. */
                kind?: string;
                /** @description How many artifacts to return (default 20). */
                limit?: number;
            };
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The artifacts. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunArtifacts"];
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
    cancelRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The run as it is after the request. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Run"];
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
    replyToRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The run's TypeID. */
                id: components["parameters"]["RunIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ReplyRequest"];
            };
        };
        responses: {
            /** @description The run, queued again. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Run"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description `run_not_waiting`: the run is not waiting for an answer. */
            409: {
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
    listRenders: {
        parameters: {
            query?: {
                /** @description How many renders to return (default 50). */
                limit?: number;
                /** @description The `next_cursor` of the previous page. */
                cursor?: string;
                /** @description Only renders of this timeline version. */
                version?: number;
                /** @description Only renders of this profile. */
                profile?: components["schemas"]["RenderProfile"];
            };
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A page of renders. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListRendersResponse"];
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
    createRender: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The project's TypeID. */
                id: components["parameters"]["ProjectIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateRenderRequest"];
            };
        };
        responses: {
            /** @description The existing render of that version and profile. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Render"];
                };
            };
            /** @description A new render was queued. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Render"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthenticated"];
            402: components["responses"]["PlanRequired"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /**
             * @description `timeline_invalid`: the version does not validate against the project's footage now. Or `edit_not_charged`: a final
             *     render of a project that has no charged edit. Or `version_not_charged`: a final render of a version that holds editor
             *     work that was not charged.
             */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Error"];
                };
            };
            /** @description `too_many_renders`: the workspace already has 5 renders waiting or running. */
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
    getRender: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The render's TypeID. */
                id: components["parameters"]["RenderIdPath"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The render. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Render"];
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
    listAnnotations: {
        parameters: {
            query?: {
                target_type?: components["schemas"]["AnnotationTargetType"];
                /** @description The TypeID of the target (`ast_`, `ren_` or `tl_` after `target_type`). */
                target_id?: string;
                project_id?: components["schemas"]["ProjectId"];
                /** @description How many comments to return (default 200). */
                limit?: number;
                /** @description The `next_cursor` of the previous page. */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A page of comments. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListAnnotationsResponse"];
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
    createAnnotation: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateAnnotationRequest"];
            };
        };
        responses: {
            /** @description The comment. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Annotation"];
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
    deleteAnnotation: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The annotation's TypeID. */
                id: components["parameters"]["AnnotationIdPath"];
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
    updateAnnotation: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description The annotation's TypeID. */
                id: components["parameters"]["AnnotationIdPath"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["UpdateAnnotationRequest"];
            };
        };
        responses: {
            /** @description The comment as it is now. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Annotation"];
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
}
