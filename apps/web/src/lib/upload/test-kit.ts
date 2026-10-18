/**
 * Fakes for the upload tests: an in-memory model of the assets API (the same part plan, limits and error codes as the Go
 * server), a transport that "uploads" to it, a clock that only moves when something sleeps, and files of any size
 * without bytes.
 */
import { ApiError } from "@/lib/api/client";
import type { ApiAsset, CompletedPartRef, CreateAssetRequest, CreateAssetResponse, PresignPartsResponse, UploadApi, UploadStateResponse } from "./api";
import { partCountOf, partSizeOf } from "./plan";
import type { Clock, Connectivity } from "./timing";
import { type PartTransport, PutError } from "./transport";

export const MiB = 1024 * 1024;

export function fakeFile(name: string, size: number, init: { type?: string; lastModified?: number } = {}): File {
  const file = new File([], name, { type: init.type ?? "video/mp4", lastModified: init.lastModified ?? 1_700_000_000_000 });
  Object.defineProperty(file, "size", { value: size });
  Object.defineProperty(file, "slice", { value: (start = 0, end = size) => ({ size: Math.min(end, size) - start, arrayBuffer: async () => new ArrayBuffer(0) }) as unknown as Blob });
  return file;
}

export interface VirtualClock extends Clock {
  sleeps: number[];
  advance(ms: number): void;
}

/** Time moves only when something sleeps: a retry that waits 30 s takes no time in a test. */
export function virtualClock(start = 1_000_000): VirtualClock {
  let t = start;
  const clock: VirtualClock = {
    sleeps: [],
    now: () => t,
    random: () => 0.5,
    advance: (ms) => {
      t += ms;
    },
    async sleep(ms, signal) {
      if (signal?.aborted) throw signal.reason;
      clock.sleeps.push(ms);
      t += ms;
      // A macrotask, so that a retry loop cannot starve the test of the chance to change the world.
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      if (signal?.aborted) throw signal.reason;
    },
  };
  return clock;
}

export const online: Connectivity = { isOnline: () => true, waitOnline: async () => {} };

interface ServerAsset {
  asset: ApiAsset;
  uploadId: string;
  status: "active" | "completed" | "aborted" | "expired";
  partSize: number;
  partCount: number;
  parts: Map<number, { etag: string; size: number }>;
}

export class FakeServer implements UploadApi {
  partSize = 16 * MiB;
  maxActive = 25;
  /** The created_at of the assets made from now on. */
  createdAt = "2026-10-17T10:00:00Z";
  assets = new Map<string, ServerAsset>();
  /** Every call, "method:detail". */
  calls: string[] = [];
  /** Errors to throw on the next calls of a method, once each. */
  failures = new Map<string, unknown[]>();
  /** Runs at the start of each create (a test frees a slot, say). */
  onCreate?: (attempt: number) => void;
  /** URLs that the object store accepts. */
  validUrls = new Set<string>();
  /** The most uploads that were in progress at once. */
  peakActive = 0;
  private createAttempts = 0;
  private seq = 0;
  private versions = 0;

  failNext(method: string, ...errors: unknown[]): void {
    this.failures.set(method, [...(this.failures.get(method) ?? []), ...errors]);
  }

  private take(method: string): void {
    const queue = this.failures.get(method);
    if (queue?.length) throw queue.shift();
  }

  get(id: string): ServerAsset {
    const found = this.assets.get(id);
    if (!found) throw new ApiError(404, "not_found", "no such asset");
    return found;
  }

  count(prefix: string): number {
    return this.calls.filter((call) => call.startsWith(prefix)).length;
  }

  expireAllUrls(): void {
    this.validUrls.clear();
  }

  /** The bodies of the create calls. */
  createRequests: CreateAssetRequest[] = [];

  async create(request: CreateAssetRequest): Promise<CreateAssetResponse> {
    this.calls.push("create");
    this.createRequests.push(request);
    this.onCreate?.(++this.createAttempts);
    this.take("create");
    const active = [...this.assets.values()].filter((a) => a.status === "active").length;
    if (active >= this.maxActive) throw new ApiError(429, "too_many_uploads", "25 uploads are in progress");
    const id = `ast_${String(++this.seq).padStart(4, "0")}`;
    const partCount = partCountOf(request.size_bytes, this.partSize);
    const asset: ApiAsset = {
      id,
      kind: "video",
      status: "uploading",
      filename: request.filename,
      content_type: request.content_type,
      size_bytes: request.size_bytes,
      sha256: null,
      title: request.filename,
      description: "",
      tags: [],
      failure_reason: null,
      ingest_progress: 0,
      ingest_stage: null,
      transcript_status: "none",
      created_at: this.createdAt,
      updated_at: this.createdAt,
    };
    const entry: ServerAsset = { asset, uploadId: `upl_${this.seq}`, status: "active", partSize: this.partSize, partCount, parts: new Map() };
    this.assets.set(id, entry);
    this.peakActive = Math.max(this.peakActive, active + 1);
    const first = Array.from({ length: Math.min(20, partCount) }, (_, i) => i + 1);
    return { asset, upload: this.info(entry), parts: this.urls(entry, first), urls_expire_at: "2026-10-17T10:15:00Z" };
  }

  private info(e: ServerAsset) {
    return { id: e.uploadId as never, asset_id: e.asset.id, status: e.status, part_size: e.partSize, part_count: e.partCount, expires_at: "2026-10-18T10:00:00Z" };
  }

  private urls(e: ServerAsset, numbers: number[]) {
    return numbers.map((n) => {
      const url = `https://s3.test/${e.asset.id}/${n}?v=${++this.versions}`;
      this.validUrls.add(url);
      return { part_number: n, url, size_bytes: partSizeOf(e.asset.size_bytes, e.partSize, e.partCount, n) };
    });
  }

  async state(assetId: string): Promise<UploadStateResponse> {
    this.calls.push(`state:${assetId}`);
    this.take("state");
    const e = this.get(assetId);
    const completed = e.status === "active" ? [...e.parts].map(([n, p]) => ({ part_number: n, etag: p.etag, size_bytes: p.size })).sort((a, b) => a.part_number - b.part_number) : [];
    return { upload: this.info(e), completed_parts: completed };
  }

  async presign(assetId: string, partNumbers: number[]): Promise<PresignPartsResponse> {
    this.calls.push(`presign:${assetId}:${partNumbers.join(",")}`);
    this.take("presign");
    const e = this.get(assetId);
    if (e.status !== "active") throw new ApiError(409, "upload_not_active", "the upload is not active");
    return { parts: this.urls(e, partNumbers), urls_expire_at: "2026-10-17T10:15:00Z" };
  }

  async complete(assetId: string, parts: CompletedPartRef[]): Promise<ApiAsset> {
    this.calls.push(`complete:${assetId}`);
    this.take("complete");
    const e = this.get(assetId);
    if (e.status === "completed") return e.asset;
    if (e.status !== "active") throw new ApiError(409, "upload_not_active", "the upload is not active");
    const ok = parts.length === e.partCount && parts.every((p, i) => p.part_number === i + 1 && e.parts.get(p.part_number)?.etag === p.etag);
    if (!ok) throw new ApiError(422, "parts_mismatch", "the parts do not match");
    e.status = "completed";
    e.asset = { ...e.asset, status: "uploaded" };
    return e.asset;
  }

  async abort(assetId: string): Promise<void> {
    this.calls.push(`abort:${assetId}`);
    this.take("abort");
    const e = this.get(assetId);
    if (e.status === "completed") throw new ApiError(409, "upload_not_active", "completed");
    if (e.status === "aborted") throw new ApiError(404, "not_found", "gone");
    e.status = "aborted";
  }

  async asset(assetId: string): Promise<ApiAsset> {
    this.calls.push(`asset:${assetId}`);
    return this.get(assetId).asset;
  }

  async recent(): Promise<ApiAsset[]> {
    this.calls.push("recent");
    return [...this.assets.values()].map((e) => ({ ...e.asset, status: e.status === "active" ? ("uploading" as const) : e.asset.status })).reverse();
  }
}

export type PutScript = (context: { url: string; assetId: string; part: number; size: number; attempt: number }) => PutError | undefined | Promise<PutError | undefined>;

/** "Uploads" to a {@link FakeServer}: checks the URL and the exact Content-Length, like the object store does. */
export class FakeTransport implements PartTransport {
  puts: { assetId: string; part: number; size: number; url: string }[] = [];
  inflight = 0;
  maxInflight = 0;
  /** Decides per attempt whether the PUT fails. */
  script?: PutScript;
  /** Holds a PUT until released (or aborted). */
  hold?: (context: { assetId: string; part: number }) => Promise<void> | undefined;
  /** A failing PUT first reports some bytes sent (a connection that dropped part way). */
  progressBeforeFailing = false;
  private attempts = new Map<string, number>();
  private etag = 0;

  constructor(private readonly server: FakeServer) {}

  async put(url: string, body: Blob, { signal, onProgress }: { signal: AbortSignal; onProgress(loaded: number): void }): Promise<string | null> {
    const match = /^https:\/\/s3\.test\/(ast_\d+)\/(\d+)\?/.exec(url);
    if (!match) throw new PutError(403, "bad url", "AccessDenied");
    const assetId = match[1] as string;
    const part = Number(match[2]);
    const key = `${assetId}/${part}`;
    const attempt = this.attempts.get(key) ?? 0;
    this.attempts.set(key, attempt + 1);
    this.inflight++;
    this.maxInflight = Math.max(this.maxInflight, this.inflight);
    try {
      if (signal.aborted) throw signal.reason;
      const scripted = await this.script?.({ url, assetId, part, size: body.size, attempt });
      if (scripted && this.progressBeforeFailing) onProgress(Math.floor(body.size / 3));
      if (scripted) throw scripted;
      onProgress(Math.floor(body.size / 2));
      const wait = this.hold?.({ assetId, part });
      if (wait) {
        await new Promise<void>((resolve, reject) => {
          signal.addEventListener("abort", () => reject(signal.reason), { once: true });
          void wait.then(resolve);
        });
      }
      await Promise.resolve();
      if (signal.aborted) throw signal.reason;
      if (!this.server.validUrls.has(url)) throw new PutError(403, "Object storage answered 403 (AccessDenied)", "AccessDenied");
      const entry = this.server.get(assetId);
      const expected = partSizeOf(entry.asset.size_bytes, entry.partSize, entry.partCount, part);
      if (body.size !== expected) throw new PutError(403, "Object storage answered 403 (SignatureDoesNotMatch)", "SignatureDoesNotMatch");
      const etag = `"etag-${++this.etag}"`;
      entry.parts.set(part, { etag, size: body.size });
      this.puts.push({ assetId, part, size: body.size, url });
      onProgress(body.size);
      return etag;
    } finally {
      this.inflight--;
    }
  }
}
