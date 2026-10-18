/**
 * The upload manager: a queue of files, each uploaded as a multipart upload straight to object storage (D-12, P1-01).
 *
 *  - Queue: `maxFiles` files at a time, `maxPartsPerFile` parts of each in parallel. The rest wait locally (so the server's
 *    25 uploads per workspace are only reached by other devices or leftovers; a 429 `too_many_uploads` is waited out with
 *    backoff, not failed).
 *  - Retries: every part is tried again with exponential backoff and jitter. A 403 means the presigned URL expired or was
 *    refused, so a new one is asked for (`POST /parts`). A lost connection waits for the browser to be online again.
 *  - Resume: the server knows which parts it holds (`GET /upload`). That list, not our memory, decides what is uploaded
 *    again, on a retry, after a reload and before the final complete. The queue itself lives in IndexedDB; after a reload
 *    an upload waits (`needs-file`) until the same file is added again.
 *  - Cancel aborts the requests in flight and the upload on the server.
 *
 * Nothing here knows React; `./provider` wraps it.
 */
import { ApiError } from "@/lib/api/client";
import { type ApiAsset, type CompletedPartRef, isTransientApiError, type UploadApi, type UploadStateResponse } from "./api";
import { contentTypeOf, fileKey, type PickedFile, readHandle, rejectionOf } from "./files";
import { checkPlan, partRange, partSizeOf } from "./plan";
import type { UploadRecord, UploadStore } from "./store";
import { backoffMs, browserConnectivity, type Clock, type Connectivity, realClock } from "./timing";
import { type PartTransport, PutError } from "./transport";

export type UploadStatus = "queued" | "starting" | "uploading" | "completing" | "done" | "failed" | "needs-file";

export interface UploadItem {
  /** Local id; stable across reloads. */
  id: string;
  /** `style_reference` for a style video (P6-05b); absent for footage. */
  purpose?: "style_reference";
  /** The server's asset id, once created. */
  assetId: string | null;
  name: string;
  size: number;
  status: UploadStatus;
  /** 0 to 1. */
  progress: number;
  bytesDone: number;
  /** Bytes per second over the last few seconds; 0 when idle. */
  speed: number;
  etaSeconds: number | null;
  partsDone: number;
  partCount: number | null;
  error: { code: string; message: string } | null;
  /** A passing remark: "Retrying in 4 s", "Waiting for a free upload slot". */
  note: string | null;
}

export interface UploadSummary {
  total: number;
  /** starting, uploading or completing */
  active: number;
  queued: number;
  needsFile: number;
  failed: number;
  done: number;
  bytesTotal: number;
  bytesDone: number;
  speed: number;
  etaSeconds: number | null;
}

export interface UploadSnapshot {
  items: readonly UploadItem[];
  summary: UploadSummary;
  /** Whether the queue was read back from storage yet. */
  ready: boolean;
}

export type UploadEvent =
  | { type: "created"; assetId: string }
  | { type: "completed"; assetId: string; asset: ApiAsset }
  | { type: "removed"; assetId: string };

export interface AddResult {
  added: number;
  /** Files that matched an interrupted upload and continue it. */
  resumed: number;
  /** Files already in the queue. */
  duplicates: number;
  rejected: { name: string; reason: string }[];
}

export interface ManagerOptions {
  /** Files uploading at once. */
  maxFiles: number;
  /** Parts of one file uploading at once. */
  maxPartsPerFile: number;
  /** Tries per part before the file fails. */
  maxPartAttempts: number;
  /** Tries per API call before the file fails (for server hiccups; see networkGraceMs for lost connections). */
  maxApiAttempts: number;
  /** How long a lost connection is waited out, part by part and call by call, before the file fails. */
  networkGraceMs: number;
  /** How many part URLs one /parts call asks for (the API takes 100). */
  presignBatch: number;
  /** Part URLs are valid for 15 minutes; they are renewed after this many ms. */
  urlTtlMs: number;
  /** A URL this close to its end is renewed before it is used. */
  urlSkewMs: number;
  /** The window of the speed estimate. */
  speedWindowMs: number;
  /** UI updates are batched into one per this many ms (0: at once). */
  emitIntervalMs: number;
  /** Times a file starts over because the server dropped its upload (expired, aborted elsewhere). */
  maxRestarts: number;
}

export const DEFAULT_OPTIONS: ManagerOptions = {
  maxFiles: 3,
  maxPartsPerFile: 4,
  maxPartAttempts: 8,
  maxApiAttempts: 6,
  networkGraceMs: 10 * 60_000,
  presignBatch: 20,
  urlTtlMs: 14 * 60_000,
  urlSkewMs: 30_000,
  speedWindowMs: 5_000,
  emitIntervalMs: 150,
  maxRestarts: 2,
};

export interface ManagerDeps {
  api: UploadApi;
  transport: PartTransport;
  store: UploadStore;
  /** `userId:orgId`. */
  workspace: string;
  clock?: Clock;
  connectivity?: Connectivity;
  options?: Partial<ManagerOptions>;
  newId?: () => string;
  /** Called with every API error that ended an upload (the app refetches `me` on a 402). */
  onApiError?: (error: unknown) => void;
}

/** A failure to show: stable `code`, human `message`. */
export class UploadFailure extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(message);
    this.name = "UploadFailure";
    this.code = code;
  }
}
class CanceledError extends Error {
  constructor() {
    super("canceled");
    this.name = "CanceledError";
  }
}
class DisposedError extends Error {
  constructor() {
    super("disposed");
    this.name = "DisposedError";
  }
}
/** The server dropped this upload; the file starts over with a new one. */
class RestartError extends Error {
  constructor() {
    super("restart");
    this.name = "RestartError";
  }
}
/** Not now: the file goes back in the queue and waits. */
class DeferError extends Error {
  readonly delayMs: number;
  constructor(delayMs: number) {
    super("defer");
    this.name = "DeferError";
    this.delayMs = delayMs;
  }
}

interface Item {
  id: string;
  rec: UploadRecord;
  file: File | null;
  status: UploadStatus;
  note: string | null;
  partsDone: number;
  completedBytes: number;
  inflight: Map<number, number>;
  /** Every byte put on the wire, retried ones too: the speed is measured on it. */
  sent: number;
  samples: { t: number; sent: number }[];
  notBefore: number;
  createAttempts: number;
  restarts: number;
  ctrl: AbortController | null;
  removed: boolean;
}

/** What a running upload knows about its parts. */
interface Job {
  assetId: string;
  partSize: number;
  partCount: number;
  size: number;
  done: Set<number>;
  urls: Map<number, { url: string; expiresAt: number }>;
  presigning: Promise<void> | null;
}

/** The browser's and the server's clocks may differ by this much when the time of a create call is compared with a created_at. */
const CLOCK_SKEW_MS = 2 * 60_000;

const WARN = (message: string, error: unknown) => console.warn(`[uploads] ${message}`, error);

export class UploadManager {
  private readonly api: UploadApi;
  private readonly transport: PartTransport;
  private readonly store: UploadStore;
  private readonly workspace: string;
  private readonly clock: Clock;
  private readonly net: Connectivity;
  private readonly opts: ManagerOptions;
  private readonly newId: () => string;
  private readonly onApiError?: (error: unknown) => void;

  private items: Item[] = [];
  private snapshot: UploadSnapshot = { items: [], summary: emptySummary(), ready: false };
  private listeners = new Set<() => void>();
  private eventListeners = new Set<(event: UploadEvent) => void>();
  private emitTimer: ReturnType<typeof setTimeout> | undefined;
  private ticker: ReturnType<typeof setInterval> | undefined;
  private persistTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private createBlockedUntil = 0;
  private wakeAt = Number.POSITIVE_INFINITY;
  private readonly life = new AbortController();
  private disposed = false;
  private isReady = false;
  readonly ready: Promise<void>;
  private resolveReady!: () => void;

  constructor(deps: ManagerDeps) {
    this.api = deps.api;
    this.transport = deps.transport;
    this.store = deps.store;
    this.workspace = deps.workspace;
    this.clock = deps.clock ?? realClock;
    this.net = deps.connectivity ?? browserConnectivity;
    this.opts = { ...DEFAULT_OPTIONS, ...deps.options };
    this.newId = deps.newId ?? (() => crypto.randomUUID());
    this.onApiError = deps.onApiError;
    this.ready = new Promise<void>((resolve) => {
      this.resolveReady = resolve;
    });
  }

  // ── Subscriptions ─────────────────────────────────────────────────────────

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** A stable object between changes, for `useSyncExternalStore`. */
  getSnapshot = (): UploadSnapshot => this.snapshot;

  subscribeEvents(listener: (event: UploadEvent) => void): () => void {
    this.eventListeners.add(listener);
    return () => this.eventListeners.delete(listener);
  }

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  /** Reads the interrupted uploads of this workspace back from storage. They wait for their files. */
  async init(): Promise<void> {
    try {
      const records = await this.store.list(this.workspace);
      records.sort((a, b) => a.createdAt - b.createdAt);
      for (const rec of records) {
        if (this.items.some((item) => item.id === rec.id)) continue;
        this.items.push(this.newItem(rec, null, rec.error ? "failed" : "needs-file"));
      }
    } catch (error) {
      WARN("could not read the stored queue", error);
    }
    // Where the browser lets a stored handle be read without asking, the interrupted uploads continue by themselves.
    await Promise.all(
      this.items
        .filter((item) => item.file === null && item.rec.handle !== undefined)
        .map(async (item) => {
          const file = await readHandle(item.rec.handle);
          if (file && sameFile(file, item.rec) && item.file === null) this.attach(item, file);
        }),
    );
    this.isReady = true;
    this.resolveReady();
    this.flush(); // at once: the tray should not wait for the batching timer to show what was stored
    this.pump();
  }

  /** Stops everything that is running without touching the stored queue (a workspace switch, the page closing). */
  dispose(): void {
    this.disposed = true;
    this.life.abort(new DisposedError());
    for (const item of this.items) item.ctrl?.abort(new DisposedError());
    for (const timer of this.persistTimers.values()) clearTimeout(timer);
    this.persistTimers.clear();
    clearTimeout(this.emitTimer);
    clearInterval(this.ticker);
    this.listeners.clear();
    this.eventListeners.clear();
  }

  // ── Commands ──────────────────────────────────────────────────────────────

  /**
   * Adds files to the queue. A file that is an interrupted upload (same name, size and date) continues that upload. `purpose:
   * "style_reference"` uploads them as the creator's style videos, which are never footage.
   */
  async add(picked: readonly (File | PickedFile)[], options: { purpose?: "style_reference" } = {}): Promise<AddResult> {
    await this.ready;
    const result: AddResult = { added: 0, resumed: 0, duplicates: 0, rejected: [] };
    for (const entry of picked) {
      const { file, handle } = "file" in entry ? entry : { file: entry, handle: undefined };
      const existing = this.items.find((item) => fileKey(item.rec) === fileKey(file) && item.status !== "done");
      if (existing) {
        if (existing.file === null || existing.status === "failed") {
          // The file of an interrupted upload, or of one that failed: it continues (a failed one is tried again).
          if (handle !== undefined) existing.rec.handle = handle;
          existing.file = file;
          if (existing.status === "failed") this.retry(existing.id);
          else this.attach(existing, file);
          this.persistNow(existing);
          result.resumed++;
        } else {
          result.duplicates++;
        }
        continue;
      }
      const reason = rejectionOf(file);
      const type = contentTypeOf(file);
      if (reason || !type) {
        result.rejected.push({ name: file.name, reason: reason ?? "This file type cannot be uploaded." });
        continue;
      }
      const rec: UploadRecord = {
        id: this.newId(),
        workspace: this.workspace,
        assetId: null,
        createStartedAt: null,
        name: file.name,
        size: file.size,
        lastModified: file.lastModified,
        type,
        partSize: null,
        partCount: null,
        bytesDone: 0,
        error: null,
        createdAt: this.clock.now(),
        handle,
        ...(options.purpose ? { purpose: options.purpose } : {}),
      };
      const item = this.newItem(rec, file, "queued");
      this.items.push(item);
      this.persistNow(item);
      result.added++;
    }
    this.dirty();
    this.pump();
    return result;
  }

  /**
   * Stops an upload and removes it, on the server too (so that it stops counting towards the 25), unless
   * `abortOnServer` is false because the caller deletes the asset itself. A finished one is just dismissed.
   */
  cancel(id: string, { abortOnServer = true }: { abortOnServer?: boolean } = {}): void {
    const item = this.items.find((candidate) => candidate.id === id);
    if (!item) return;
    const wasDone = item.status === "done";
    item.removed = true;
    item.ctrl?.abort(new CanceledError());
    this.items = this.items.filter((candidate) => candidate !== item);
    clearTimeout(this.persistTimers.get(id));
    this.persistTimers.delete(id);
    void this.store.delete(id).catch((error) => WARN("could not delete a stored upload", error));
    const assetId = item.rec.assetId;
    if (abortOnServer && !wasDone) {
      if (assetId) void this.abortOnServer(assetId).then(() => this.emitEvent({ type: "removed", assetId }));
      // A create call that may have made an asset without our knowing: find it and abort it too.
      else if (item.rec.createStartedAt !== null) void this.abortCreated(item.rec);
    }
    if (assetId && !abortOnServer) this.emitEvent({ type: "removed", assetId });
    this.dirty();
    this.pump();
  }

  /** Tries a failed upload again. If the page was reloaded since, it waits for the file first. */
  retry(id: string): void {
    const item = this.items.find((candidate) => candidate.id === id);
    if (item?.status !== "failed") return;
    item.rec.error = null;
    item.note = null;
    item.createAttempts = 0;
    item.restarts = 0;
    item.notBefore = 0;
    item.status = item.file ? "queued" : "needs-file";
    this.persistNow(item);
    this.dirty();
    this.pump();
  }

  retryAllFailed(): void {
    for (const item of this.items) if (item.status === "failed") this.retry(item.id);
  }

  /** Removes the finished uploads from the list. */
  clearDone(): void {
    this.items = this.items.filter((item) => item.status !== "done");
    this.dirty();
  }

  // ── Scheduling ────────────────────────────────────────────────────────────

  private newItem(rec: UploadRecord, file: File | null, status: UploadStatus): Item {
    return {
      id: rec.id,
      rec,
      file,
      status,
      note: null,
      partsDone: 0,
      completedBytes: rec.bytesDone,
      inflight: new Map(),
      sent: 0,
      samples: [],
      notBefore: 0,
      createAttempts: 0,
      restarts: 0,
      ctrl: null,
      removed: false,
    };
  }

  /** Gives an interrupted or failed upload its file again. */
  private attach(item: Item, file: File): void {
    item.file = file;
    if (item.status === "needs-file") item.status = "queued";
    this.dirty();
    this.pump();
  }

  private pump(): void {
    if (this.disposed || !this.isReady) return;
    const now = this.clock.now();
    let running = this.items.filter((item) => item.ctrl !== null).length;
    let wake = Number.POSITIVE_INFINITY;
    for (const item of this.items) {
      if (running >= this.opts.maxFiles) break;
      if (item.status !== "queued" || item.file === null || item.ctrl !== null) continue;
      const due = Math.max(item.notBefore, item.rec.assetId ? 0 : this.createBlockedUntil);
      if (due > now) {
        wake = Math.min(wake, due);
        continue;
      }
      this.start(item);
      running++;
    }
    if (wake < Number.POSITIVE_INFINITY && wake < this.wakeAt) {
      this.wakeAt = wake;
      void this.clock.sleep(wake - now, this.life.signal).then(
        () => {
          this.wakeAt = Number.POSITIVE_INFINITY;
          this.pump();
        },
        () => {},
      );
    }
  }

  private start(item: Item): void {
    const ctrl = new AbortController();
    item.ctrl = ctrl;
    item.status = "starting";
    item.note = null;
    this.dirty();
    void this.run(item, ctrl);
  }

  private async run(item: Item, ctrl: AbortController): Promise<void> {
    try {
      await this.execute(item, ctrl.signal);
    } catch (error) {
      if (item.removed || this.disposed) {
        // canceled or the manager is gone: nothing to report
      } else if (error instanceof DeferError) {
        item.status = "queued";
        item.notBefore = this.clock.now() + error.delayMs;
      } else {
        this.fail(item, error);
      }
    } finally {
      item.ctrl = null;
      item.inflight.clear();
      if (!item.removed && !this.disposed) this.persistNow(item);
      this.dirty();
      this.pump();
    }
  }

  // ── One upload ────────────────────────────────────────────────────────────

  private async execute(item: Item, signal: AbortSignal): Promise<void> {
    const file = item.file;
    if (!file) throw new UploadFailure("file_missing", "The file is not available.");
    for (;;) {
      const job = await this.prepare(item, signal);
      if (!job) return; // it was complete already
      try {
        await this.transfer(item, job, file, signal);
        return;
      } catch (error) {
        if (error instanceof RestartError && item.restarts < this.opts.maxRestarts) {
          item.restarts++;
          await this.restart(item);
          continue;
        }
        if (error instanceof RestartError) throw new UploadFailure("upload_gone", "The upload no longer exists on the server. Try again.");
        throw error;
      }
    }
  }

  /** Creates the upload, or looks up the one a reload interrupted. Returns null when it turns out to be complete. */
  private async prepare(item: Item, signal: AbortSignal): Promise<Job | null> {
    const { rec } = item;
    item.status = "starting";
    this.dirty();
    if (rec.assetId) {
      let state: UploadStateResponse | null;
      try {
        state = await this.call(item, signal, () => this.api.state(rec.assetId as string, signal));
      } catch (error) {
        if (!(error instanceof ApiError && error.status === 404)) throw error;
        state = null;
      }
      if (state?.upload.status === "completed") {
        this.finish(item, await this.call(item, signal, () => this.api.asset(rec.assetId as string, signal)));
        return null;
      }
      if (state?.upload.status === "active") return this.jobFromState(item, state, new Map());
      await this.restart(item);
    }
    return this.createUpload(item, signal);
  }

  private async createUpload(item: Item, signal: AbortSignal): Promise<Job> {
    const { rec } = item;
    let since = -1;
    for (let attempt = 0; ; attempt++) {
      throwIfAborted(signal);
      if (rec.createStartedAt !== null) {
        // A create call may have gone through without our hearing back (a reload, a dropped connection, a 5xx): use its asset.
        const adopted = await this.adopt(item, signal);
        if (adopted) return adopted;
      }
      rec.createStartedAt ??= this.clock.now();
      await this.store.put(rec).catch((error) => WARN("could not store an upload", error));
      throwIfAborted(signal);
      try {
        const created = this.api.create({ filename: rec.name, size_bytes: rec.size, content_type: rec.type, ...(rec.purpose ? { purpose: rec.purpose } : {}) });
        // The call itself is not abortable (the server may have made the asset), so a cancel afterwards removes it.
        created.then(
          (response) => {
            if (item.removed) void this.abortOnServer(response.asset.id);
          },
          () => {},
        );
        const response = await raceAbort(created, signal);
        const received = this.clock.now();
        rec.assetId = response.asset.id;
        rec.createStartedAt = null;
        rec.partSize = response.upload.part_size;
        rec.partCount = response.upload.part_count;
        await this.store.put(rec).catch((error) => WARN("could not store an upload", error));
        this.emitEvent({ type: "created", assetId: response.asset.id });
        const urls = new Map(response.parts.map((part) => [part.part_number, { url: part.url, expiresAt: received + this.opts.urlTtlMs }]));
        return this.jobFromState(item, { upload: response.upload, completed_parts: [] }, urls);
      } catch (error) {
        // A definite refusal (429, 4xx) means no asset was made: nothing to look for later.
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
          rec.createStartedAt = null;
          await this.store.put(rec).catch((e) => WARN("could not store an upload", e));
        }
        if (error instanceof ApiError && error.code === "too_many_uploads") {
          // The workspace has its 25 uploads in progress (other devices, leftovers): wait, and let the others wait too.
          const delay = backoffMs(item.createAttempts++, this.clock.random(), 2_000, 30_000);
          this.createBlockedUntil = Math.max(this.createBlockedUntil, this.clock.now() + delay);
          item.note = "Waiting for a free upload slot";
          throw new DeferError(delay);
        }
        if (since < 0) since = this.clock.now();
        if (this.worthRetrying(error, attempt, since) && !signal.aborted) {
          await this.backoff(item, attempt, signal, "Reconnecting");
          continue;
        }
        throw error;
      }
    }
  }

  /**
   * The assets that a create call whose answer was lost may have made: still uploading, this file's name and size, made
   * after the call was sent (with room for the clocks of the browser and the server to differ), and not another
   * upload's. Only exactly one candidate is trusted; with none or several a new upload is made instead.
   */
  private createdCandidates(rec: UploadRecord, recent: readonly ApiAsset[]): ApiAsset[] {
    const claimed = new Set(this.items.map((other) => other.rec.assetId));
    const since = (rec.createStartedAt ?? 0) - CLOCK_SKEW_MS;
    return recent.filter(
      (asset) =>
        asset.status === "uploading" && asset.size_bytes === rec.size && asset.filename === storedName(rec.name) && !claimed.has(asset.id) && Date.parse(asset.created_at) >= since,
    );
  }

  /** Finds the active upload of this file after a create call whose answer was lost. A failed look-up is an error, not a reason to create a second upload. */
  private async adopt(item: Item, signal: AbortSignal): Promise<Job | null> {
    const { rec } = item;
    const recent = await this.call(item, signal, () => this.api.recent(100, signal, rec.purpose));
    const candidates = this.createdCandidates(rec, recent);
    const stored = candidates.length === 1 ? candidates[0] : undefined;
    if (!stored) {
      rec.createStartedAt = null; // nothing of ours is there (or it is ambiguous): the next create starts clean
      return null;
    }
    rec.assetId = stored.id;
    rec.createStartedAt = null;
    await this.store.put(rec).catch((error) => WARN("could not store an upload", error));
    const state = await this.call(item, signal, () => this.api.state(stored.id, signal));
    if (state.upload.status === "active") return this.jobFromState(item, state, new Map());
    rec.assetId = null;
    return null;
  }

  /** A cancelled upload whose create call may have made an asset: look for it and abort it. */
  private async abortCreated(rec: UploadRecord): Promise<void> {
    try {
      const found = this.createdCandidates(rec, await this.api.recent(100, undefined, rec.purpose));
      const only = found.length === 1 ? found[0] : undefined;
      if (only) {
        await this.abortOnServer(only.id);
        this.emitEvent({ type: "removed", assetId: only.id });
      }
    } catch (error) {
      WARN("could not look for the asset of a cancelled upload", error);
    }
  }

  private jobFromState(item: Item, state: UploadStateResponse, urls: Map<number, { url: string; expiresAt: number }>): Job {
    const { upload } = state;
    const size = item.rec.size;
    if (!checkPlan(size, upload.part_size, upload.part_count)) {
      throw new UploadFailure("bad_plan", "The server's upload plan does not fit this file.");
    }
    item.rec.assetId = upload.asset_id;
    item.rec.partSize = upload.part_size;
    item.rec.partCount = upload.part_count;
    const job: Job = { assetId: upload.asset_id, partSize: upload.part_size, partCount: upload.part_count, size, done: new Set(), urls, presigning: null };
    this.adoptParts(item, job, state);
    this.persistNow(item);
    return job;
  }

  /** The parts the server holds are the parts that are done; one of the wrong size is not. */
  private adoptParts(item: Item, job: Job, state: UploadStateResponse): void {
    job.done = new Set(
      state.completed_parts
        .filter((part) => part.part_number >= 1 && part.part_number <= job.partCount && part.size_bytes === partSizeOf(job.size, job.partSize, job.partCount, part.part_number))
        .map((part) => part.part_number),
    );
    item.partsDone = job.done.size;
    item.completedBytes = sumParts(job, job.done);
    item.rec.bytesDone = item.completedBytes;
  }

  private async transfer(item: Item, job: Job, file: File, signal: AbortSignal): Promise<void> {
    if (missingParts(job).length > 0) await this.uploadParts(item, job, file, missingParts(job), signal);
    // The server's list decides what is left; ListParts is also what the final complete is checked against.
    for (let round = 0; round < 4; round++) {
      this.setStatus(item, "completing");
      const state = await this.call(item, signal, () => this.api.state(job.assetId, signal));
      if (state.upload.status === "completed") {
        this.finish(item, await this.call(item, signal, () => this.api.asset(job.assetId, signal)));
        return;
      }
      if (state.upload.status !== "active") throw new RestartError();
      this.adoptParts(item, job, state);
      const missing = missingParts(job);
      if (missing.length > 0) {
        await this.uploadParts(item, job, file, missing, signal);
        continue;
      }
      const parts: CompletedPartRef[] = state.completed_parts
        .filter((part) => job.done.has(part.part_number))
        .sort((a, b) => a.part_number - b.part_number)
        .map((part) => ({ part_number: part.part_number, etag: part.etag }));
      try {
        this.finish(item, await this.call(item, signal, () => this.api.complete(job.assetId, parts, signal)));
        return;
      } catch (error) {
        if (!(error instanceof ApiError)) throw error;
        if (error.code === "parts_mismatch") continue; // read the list again
        if (error.code === "upload_expired" || error.code === "upload_not_active" || error.status === 404) {
          const asset = await this.api.asset(job.assetId, signal).catch(() => null);
          if (asset && (asset.status === "uploaded" || asset.status === "processing" || asset.status === "ready")) {
            this.finish(item, asset);
            return;
          }
          throw new RestartError();
        }
        if (error.code === "size_mismatch") throw new UploadFailure("size_mismatch", "The file changed while it was uploading. Add it again.");
        throw error;
      }
    }
    throw new UploadFailure("verify_failed", "The server did not accept the uploaded parts. Try again.");
  }

  /** Uploads these parts, `maxPartsPerFile` at a time, in order. The first part to fail for good stops the others. */
  private async uploadParts(item: Item, job: Job, file: File, parts: number[], signal: AbortSignal): Promise<void> {
    this.setStatus(item, "uploading");
    const inner = new AbortController();
    const stop = () => inner.abort(signal.reason);
    if (signal.aborted) stop();
    signal.addEventListener("abort", stop, { once: true });
    const queue = [...parts];
    let failure: { error: unknown } | null = null;
    const worker = async () => {
      while (!inner.signal.aborted) {
        const n = queue.shift();
        if (n === undefined) return;
        try {
          await this.uploadPart(item, job, file, n, inner.signal);
        } catch (error) {
          failure ??= { error };
          inner.abort(error);
          return;
        }
      }
    };
    try {
      await Promise.all(Array.from({ length: Math.min(this.opts.maxPartsPerFile, parts.length) }, worker));
    } finally {
      signal.removeEventListener("abort", stop);
    }
    if (failure) throw (failure as { error: unknown }).error;
    throwIfAborted(signal);
  }

  private async uploadPart(item: Item, job: Job, file: File, n: number, signal: AbortSignal): Promise<void> {
    const { start, end } = partRange(job.size, job.partSize, job.partCount, n);
    let lastProgress = this.clock.now(); // the grace for a lost connection counts from the last byte this part sent
    for (let attempt = 0; ; attempt++) {
      throwIfAborted(signal);
      await this.net.waitOnline(signal);
      let failed: PutError | ApiError | null = null;
      try {
        const url = await this.urlFor(item, job, n, signal);
        await this.transport.put(url, file.slice(start, end), {
          signal,
          onProgress: (loaded) => {
            lastProgress = this.clock.now();
            this.progress(item, n, loaded);
          },
        });
        item.inflight.delete(n);
        job.done.add(n);
        item.partsDone = job.done.size;
        item.completedBytes += end - start;
        item.rec.bytesDone = item.completedBytes;
        item.note = null;
        this.persistSoon(item);
        this.dirty();
        return;
      } catch (error) {
        item.inflight.delete(n);
        if (signal.aborted) throw signal.reason;
        if (error instanceof PutError) {
          failed = error;
          if (error.status === 403) job.urls.delete(n); // expired or refused: ask for a new one
          else if (error.status === 404 && error.s3Code === "NoSuchUpload") throw new RestartError();
          else if (!isTransientPut(error)) throw new UploadFailure("part_rejected", `Part ${n} was refused: ${error.message}`);
        } else if (error instanceof ApiError) {
          if (error.status === 404 || error.code === "upload_expired" || error.code === "upload_not_active") throw new RestartError();
          if (!isTransientApiError(error)) throw error;
          failed = error;
        } else {
          throw error;
        }
      }
      // No answer at all is waited out (the connection is down, not the part); other failures get maxPartAttempts tries.
      const noAnswer = (failed instanceof PutError || failed instanceof ApiError) && failed.status === 0;
      const exhausted = noAnswer ? this.clock.now() - lastProgress >= this.opts.networkGraceMs : attempt + 1 >= this.opts.maxPartAttempts;
      if (exhausted) {
        if (failed instanceof PutError && failed.status === 0 && !(await readable(file, start))) {
          throw new UploadFailure("file_unreadable", "The file can no longer be read. Was it moved, deleted or changed?");
        }
        throw new UploadFailure("part_failed", noAnswer ? `Part ${n} could not be uploaded: ${failed?.message}` : `Part ${n} failed ${this.opts.maxPartAttempts} times: ${failed?.message ?? "unknown error"}`);
      }
      await this.backoff(item, attempt, signal, `Retrying part ${n}`);
    }
  }

  /** The URL for part `n`: the one in hand, or a new batch (this part and the next ones) when it is missing or about to expire. */
  private async urlFor(item: Item, job: Job, n: number, signal: AbortSignal): Promise<string> {
    const size = partSizeOf(job.size, job.partSize, job.partCount, n);
    for (;;) {
      throwIfAborted(signal);
      const have = job.urls.get(n);
      if (have && have.expiresAt - this.clock.now() > this.opts.urlSkewMs) return have.url;
      if (job.presigning) {
        // Another part is already asking: its answer may cover this part too.
        await job.presigning.catch(() => {});
        continue;
      }
      const wanted = [n];
      for (let next = n + 1; next <= job.partCount && wanted.length < this.opts.presignBatch; next++) {
        const url = job.urls.get(next);
        if (!job.done.has(next) && (!url || url.expiresAt - this.clock.now() <= this.opts.urlSkewMs)) wanted.push(next);
      }
      const asking = (async () => {
        const response = await this.call(item, signal, () => this.api.presign(job.assetId, wanted, signal));
        const received = this.clock.now();
        for (const part of response.parts) job.urls.set(part.part_number, { url: part.url, expiresAt: received + this.opts.urlTtlMs });
        const got = response.parts.find((part) => part.part_number === n);
        if (got && got.size_bytes !== size) throw new UploadFailure("bad_plan", "The server's upload plan does not fit this file.");
      })();
      job.presigning = asking;
      try {
        await asking;
      } finally {
        job.presigning = null;
      }
      if (!job.urls.has(n)) throw new UploadFailure("bad_plan", "The server did not return the URL of a part.");
    }
  }

  private progress(item: Item, n: number, loaded: number): void {
    const before = item.inflight.get(n) ?? 0;
    item.inflight.set(n, loaded);
    if (loaded > before) {
      item.sent += loaded - before;
      item.samples.push({ t: this.clock.now(), sent: item.sent });
    }
    this.dirty();
  }

  private finish(item: Item, asset: ApiAsset): void {
    item.status = "done";
    item.note = null;
    item.completedBytes = item.rec.size;
    item.inflight.clear();
    item.rec.error = null;
    clearTimeout(this.persistTimers.get(item.id));
    this.persistTimers.delete(item.id);
    void this.store.delete(item.id).catch((error) => WARN("could not delete a stored upload", error));
    this.emitEvent({ type: "completed", assetId: asset.id, asset });
    this.dirty();
  }

  private fail(item: Item, error: unknown): void {
    const failure = describeError(error);
    item.status = "failed";
    item.note = null;
    item.rec.error = failure;
    if (error instanceof ApiError) this.onApiError?.(error);
    this.persistNow(item);
    this.dirty();
  }

  /** The upload on the server is no use any more: it is aborted there (so that it does not count towards the 25), and the next try creates a new one. */
  private async restart(item: Item): Promise<void> {
    const old = item.rec.assetId;
    this.forget(item);
    if (old) await this.abortOnServer(old);
  }

  private forget(item: Item): void {
    item.rec.assetId = null;
    item.rec.createStartedAt = null;
    item.rec.partSize = null;
    item.rec.partCount = null;
    item.rec.bytesDone = 0;
    item.completedBytes = 0;
    item.partsDone = 0;
    this.persistNow(item);
  }

  // ── Helpers ───────────────────────────────────────────────────────────────

  /** An API call tried again while the failure is a passing one. */
  private async call<T>(item: Item, signal: AbortSignal, fn: () => Promise<T>): Promise<T> {
    let since = -1;
    for (let attempt = 0; ; attempt++) {
      throwIfAborted(signal);
      try {
        return await fn();
      } catch (error) {
        if (signal.aborted) throw signal.reason;
        if (since < 0) since = this.clock.now();
        if (!this.worthRetrying(error, attempt, since)) throw error;
        await this.backoff(item, attempt, signal, "Reconnecting");
      }
    }
  }

  /** Whether a failed API call is worth another try: a lost connection is waited out for networkGraceMs, other hiccups get a few tries. */
  private worthRetrying(error: unknown, attempt: number, since: number): boolean {
    if (!isTransientApiError(error)) return false;
    if (error instanceof ApiError && error.status === 0) return this.clock.now() - since < this.opts.networkGraceMs;
    return attempt + 1 < this.opts.maxApiAttempts;
  }

  private async backoff(item: Item, attempt: number, signal: AbortSignal, what: string): Promise<void> {
    const delay = backoffMs(attempt, this.clock.random());
    const note = `${what} in ${Math.max(1, Math.round(delay / 1000))} s`;
    item.note = note;
    this.dirty();
    try {
      await this.clock.sleep(delay, signal);
    } finally {
      if (item.note === note) item.note = null;
    }
  }

  private async abortOnServer(assetId: string): Promise<void> {
    for (let attempt = 0; attempt < 3 && !this.disposed; attempt++) {
      try {
        await this.api.abort(assetId);
        return;
      } catch (error) {
        // 404 (already gone) and 409 (already completed) are final; so is anything that is not a hiccup.
        if (!isTransientApiError(error)) return;
        await this.clock.sleep(backoffMs(attempt, this.clock.random()), this.life.signal).catch(() => {});
      }
    }
  }

  private setStatus(item: Item, status: UploadStatus): void {
    if (item.status === status) return;
    item.status = status;
    this.dirty();
  }

  private persistNow(item: Item): void {
    if (item.removed || this.disposed || item.status === "done") return;
    clearTimeout(this.persistTimers.get(item.id));
    this.persistTimers.delete(item.id);
    void this.store.put(item.rec).catch((error) => WARN("could not store an upload", error));
  }

  /** The progress snapshot of the stored record is only for display, so it is saved lazily. */
  private persistSoon(item: Item): void {
    if (this.persistTimers.has(item.id)) return;
    const timer = setTimeout(
      () => {
        this.persistTimers.delete(item.id);
        this.persistNow(item);
      },
      this.opts.emitIntervalMs === 0 ? 0 : 1_000,
    );
    this.persistTimers.set(item.id, timer);
  }

  private emitEvent(event: UploadEvent): void {
    if (this.disposed) return;
    for (const listener of [...this.eventListeners]) listener(event);
  }

  // ── Snapshot ──────────────────────────────────────────────────────────────

  private dirty(): void {
    if (this.disposed) return;
    if (this.opts.emitIntervalMs === 0) {
      this.flush();
      return;
    }
    if (this.emitTimer === undefined) {
      this.emitTimer = setTimeout(() => {
        this.emitTimer = undefined;
        this.flush();
      }, this.opts.emitIntervalMs);
    }
    // While something runs the speed and the time left change even when no byte arrives: keep the view fresh.
    if (this.ticker === undefined && this.items.some((item) => item.ctrl !== null)) {
      this.ticker = setInterval(() => {
        if (this.items.some((item) => item.ctrl !== null)) this.dirty();
        else {
          clearInterval(this.ticker);
          this.ticker = undefined;
        }
      }, 500);
    }
  }

  private flush(): void {
    this.snapshot = this.buildSnapshot();
    for (const listener of [...this.listeners]) listener();
  }

  private buildSnapshot(): UploadSnapshot {
    const now = this.clock.now();
    const items = this.items.map((item) => this.view(item, now));
    const summary = emptySummary();
    for (const item of items) {
      summary.total++;
      summary.bytesTotal += item.size;
      summary.bytesDone += item.bytesDone;
      if (item.status === "starting" || item.status === "uploading" || item.status === "completing") {
        summary.active++;
        summary.speed += item.speed;
      } else if (item.status === "queued") summary.queued++;
      else if (item.status === "needs-file") summary.needsFile++;
      else if (item.status === "failed") summary.failed++;
      else summary.done++;
    }
    const pending = items.filter((item) => item.status === "queued" || item.status === "starting" || item.status === "uploading" || item.status === "completing");
    const remaining = pending.reduce((sum, item) => sum + (item.size - item.bytesDone), 0);
    summary.etaSeconds = summary.speed > 0 && pending.length > 0 ? remaining / summary.speed : null;
    return { items, summary, ready: this.isReady };
  }

  private view(item: Item, now: number): UploadItem {
    const { size } = item.rec;
    let live = 0;
    for (const loaded of item.inflight.values()) live += loaded;
    const bytesDone = item.status === "done" ? size : Math.min(size, item.completedBytes + live);
    const running = item.ctrl !== null && item.status !== "done";
    const speed = running ? speedOf(item, now, this.opts.speedWindowMs) : 0;
    return {
      id: item.id,
      assetId: item.rec.assetId,
      ...(item.rec.purpose ? { purpose: item.rec.purpose } : {}),
      name: item.rec.name,
      size,
      status: item.status,
      progress: size > 0 ? bytesDone / size : 0,
      bytesDone,
      speed,
      etaSeconds: running && speed > 0 ? (size - bytesDone) / speed : null,
      partsDone: item.partsDone,
      partCount: item.rec.partCount,
      error: item.status === "failed" ? item.rec.error : null,
      note: item.note,
    };
  }
}

function emptySummary(): UploadSummary {
  return { total: 0, active: 0, queued: 0, needsFile: 0, failed: 0, done: 0, bytesTotal: 0, bytesDone: 0, speed: 0, etaSeconds: null };
}

/** Bytes per second over the window: what went on the wire since the oldest sample in it. */
function speedOf(item: Item, now: number, windowMs: number): number {
  const keep = item.samples.findIndex((sample) => now - sample.t <= windowMs);
  item.samples = keep < 0 ? [] : item.samples.slice(keep);
  const oldest = item.samples[0];
  if (!oldest) return 0;
  const seconds = Math.max((now - oldest.t) / 1000, 0.5);
  return (item.sent - oldest.sent) / seconds;
}

function missingParts(job: Job): number[] {
  const missing: number[] = [];
  for (let n = 1; n <= job.partCount; n++) if (!job.done.has(n)) missing.push(n);
  return missing;
}

function sumParts(job: Job, parts: Iterable<number>): number {
  let sum = 0;
  for (const n of parts) sum += partSizeOf(job.size, job.partSize, job.partCount, n);
  return sum;
}

function sameFile(file: File, rec: UploadRecord): boolean {
  return fileKey(file) === fileKey(rec);
}

/** The name the server stores for `name` (a path removed, edges trimmed), as far as the client can tell. */
function storedName(name: string): string {
  return name.normalize("NFC").replace(/^.*[\\/]/, "").replace(/^[. ]+|[. ]+$/g, "");
}

function isTransientPut(error: PutError): boolean {
  return error.status === 0 || error.status === 408 || error.status === 429 || error.status >= 500;
}

/** Whether the file can still be read at `offset`; a file that was moved or changed on disk cannot. */
async function readable(file: File, offset: number): Promise<boolean> {
  try {
    await file.slice(offset, offset + 1).arrayBuffer();
    return true;
  } catch {
    return false;
  }
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw signal.reason;
}

/** `promise`, or the signal's reason as soon as it aborts. */
function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    if (signal.aborted) return reject(signal.reason);
    const onAbort = () => reject(signal.reason);
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", onAbort);
        resolve(value);
      },
      (error) => {
        signal.removeEventListener("abort", onAbort);
        reject(error);
      },
    );
  });
}

/** What the tray says about an error. */
export function describeError(error: unknown): { code: string; message: string } {
  if (error instanceof UploadFailure) return { code: error.code, message: error.message };
  if (error instanceof ApiError) {
    switch (error.code) {
      case "plan_required":
        return { code: error.code, message: "Your plan does not include uploads." };
      case "insufficient_role":
        return { code: error.code, message: "Only owners and editors can upload." };
      case "file_too_large":
        return { code: error.code, message: "Files can be 20 GB at most." };
      case "unsupported_content_type":
        return { code: error.code, message: "This file type cannot be uploaded." };
      case "unauthenticated":
      case "invalid_token":
        return { code: error.code, message: "You were signed out. Sign in again and retry." };
      case "network_error":
        return { code: error.code, message: "The server could not be reached." };
      case "storage_unavailable":
        return { code: error.code, message: "Storage is not available right now. Try again in a moment." };
      default:
        return { code: error.code, message: error.message };
    }
  }
  if (error instanceof PutError) return { code: "part_failed", message: error.message };
  return { code: "unknown_error", message: error instanceof Error ? error.message : "The upload failed." };
}
