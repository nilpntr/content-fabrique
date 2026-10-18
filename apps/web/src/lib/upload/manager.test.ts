import { describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { UploadManager, type ManagerOptions, type UploadSnapshot } from "./manager";
import { backoffMs } from "./timing";
import { FakeServer, FakeTransport, fakeFile, MiB, online, virtualClock } from "./test-kit";
import { createMemoryStore, type UploadStore } from "./store";
import { PutError } from "./transport";

let setups = 0;
function setup(
  init: { server?: FakeServer; store?: ReturnType<typeof createMemoryStore>; options?: Partial<ManagerOptions>; clock?: ReturnType<typeof virtualClock> } = {},
) {
  const server = init.server ?? new FakeServer();
  const transport = new FakeTransport(server);
  const store = init.store ?? createMemoryStore();
  const clock = init.clock ?? virtualClock();
  let n = 0;
  const prefix = `loc${setups++}`;
  const make = () =>
    new UploadManager({
      api: server,
      transport,
      store,
      workspace: "user_1:org_1",
      clock,
      connectivity: online,
      options: { emitIntervalMs: 0, ...init.options },
      newId: () => `${prefix}_${++n}`,
    });
  const manager = make();
  return { server, transport, store, clock, manager, make };
}

/** Resolves when the snapshot satisfies `predicate`. */
function until(manager: UploadManager, predicate: (snapshot: UploadSnapshot) => boolean, ms = 3000): Promise<UploadSnapshot> {
  return new Promise((resolve, reject) => {
    const check = () => {
      if (predicate(manager.getSnapshot())) {
        unsubscribe();
        clearTimeout(timer);
        resolve(manager.getSnapshot());
      }
    };
    const unsubscribe = manager.subscribe(check);
    const timer = setTimeout(() => {
      unsubscribe();
      reject(new Error(`timed out; snapshot: ${JSON.stringify(manager.getSnapshot().items.map((i) => [i.name, i.status, i.error?.code, i.note]))}`));
    }, ms);
    check();
  });
}
const settled = (s: UploadSnapshot) => s.summary.active + s.summary.queued === 0;
const doneCount = (n: number) => (s: UploadSnapshot) => s.summary.done === n;

describe("the queue", () => {
  test("style videos are uploaded as style references, and the queue says which ones they are", async () => {
    const { manager, server } = setup();
    await manager.init();
    await manager.add([fakeFile("my-video.mp4", 2 * MiB)], { purpose: "style_reference" });
    await manager.add([fakeFile("clip.mp4", 2 * MiB)]);
    await until(manager, doneCount(2));
    const byName = new Map(server.createRequests.map((request) => [request.filename, request]));
    expect(byName.get("my-video.mp4")?.purpose).toBe("style_reference");
    expect(byName.get("clip.mp4")?.purpose).toBeUndefined();
    expect(manager.getSnapshot().items.find((item) => item.name === "my-video.mp4")?.purpose).toBe("style_reference");
    expect(manager.getSnapshot().items.find((item) => item.name === "clip.mp4")?.purpose).toBeUndefined();
  });

  test("uploads every file, never more than maxFiles files and maxPartsPerFile parts at a time", async () => {
    const { manager, server, transport, store } = setup();
    await manager.init();
    const files = Array.from({ length: 7 }, (_, i) => fakeFile(`clip-${i}.mp4`, 100 * MiB + i)); // 7 parts each
    expect(await manager.add(files)).toMatchObject({ added: 7, resumed: 0, duplicates: 0, rejected: [] });
    // Only 3 are started; the others wait locally and do not take one of the server's 25 uploads.
    expect(manager.getSnapshot().summary).toMatchObject({ active: 3, queued: 4 });

    const snapshot = await until(manager, doneCount(7));
    expect(server.peakActive).toBe(3);
    expect(snapshot.summary).toMatchObject({ done: 7, failed: 0, bytesDone: snapshot.summary.bytesTotal });
    expect(transport.maxInflight).toBeLessThanOrEqual(3 * 4);
    expect(transport.maxInflight).toBeGreaterThan(4);
    expect([...server.assets.values()].every((a) => a.status === "completed" && a.parts.size === 7)).toBe(true);
    expect(transport.puts).toHaveLength(7 * 7);
    expect(store.records.size).toBe(0); // finished uploads leave the stored queue
  });

  test("cuts each file like the server plans it and completes with the ETags in part order", async () => {
    const { manager, server, transport } = setup();
    await manager.init();
    await manager.add([fakeFile("a.mp4", 50 * MiB)]);
    await until(manager, doneCount(1));
    expect(transport.puts.map((p) => [p.part, p.size]).sort((a, b) => (a[0] as number) - (b[0] as number))).toEqual([
      [1, 16 * MiB],
      [2, 16 * MiB],
      [3, 16 * MiB],
      [4, 2 * MiB],
    ]);
    const completeCall = server.calls.find((c) => c.startsWith("complete:"));
    expect(completeCall).toBeDefined();
    expect(server.get("ast_0001").parts.size).toBe(4);
  });

  test("asks for more part URLs in batches as the parts need them", async () => {
    const { manager, server } = setup();
    await manager.init();
    await manager.add([fakeFile("long.mp4", 30 * 16 * MiB)]); // 30 parts: the create call returns 20 URLs
    await until(manager, doneCount(1));
    const presigns = server.calls.filter((c) => c.startsWith("presign:"));
    expect(presigns).toHaveLength(1);
    expect(presigns[0]).toBe(`presign:ast_0001:${Array.from({ length: 10 }, (_, i) => i + 21).join(",")}`);
  });

  test("refuses files the API would refuse, and files that are already queued", async () => {
    const { manager } = setup();
    await manager.init();
    const file = fakeFile("a.mp4", 20 * MiB);
    const result = await manager.add([file, fakeFile("notes.txt", 10, { type: "text/plain" }), fakeFile("empty.mp4", 0), fakeFile("huge.mp4", 21 * 1024 ** 3)]);
    expect(result.added).toBe(1);
    expect(result.rejected.map((r) => r.name)).toEqual(["notes.txt", "empty.mp4", "huge.mp4"]);
    expect(await manager.add([file])).toMatchObject({ added: 0, duplicates: 1 });
    // A file whose type the browser leaves empty is told by its extension.
    expect(await manager.add([fakeFile("IMG_1.HEIC", 5 * MiB, { type: "" })])).toMatchObject({ added: 1 });
  });
});

describe("retries", () => {
  test("a part that fails on the network is tried again after a growing, jittered pause", async () => {
    const { manager, transport, clock } = setup();
    transport.script = ({ part, attempt }) => (part === 2 && attempt < 3 ? new PutError(0, "The connection to object storage failed.") : undefined);
    await manager.init();
    await manager.add([fakeFile("a.mp4", 40 * MiB)]);
    await until(manager, doneCount(1));
    expect(clock.sleeps).toEqual([backoffMs(0, 0.5), backoffMs(1, 0.5), backoffMs(2, 0.5)]);
    expect(clock.sleeps[1] as number).toBeGreaterThan(clock.sleeps[0] as number);
    expect(transport.puts.filter((p) => p.part === 2)).toHaveLength(1); // one success after three failures
  });

  test("5xx and 429 from object storage are retried, 400 is not", async () => {
    const { manager, transport } = setup();
    transport.script = ({ part, attempt }) => (part === 1 && attempt === 0 ? new PutError(503, "busy") : part === 2 && attempt === 0 ? new PutError(429, "slow down") : undefined);
    await manager.init();
    await manager.add([fakeFile("a.mp4", 40 * MiB)]);
    await until(manager, doneCount(1));

    const second = setup();
    second.transport.script = () => new PutError(400, "Object storage answered 400 (EntityTooLarge)", "EntityTooLarge");
    await second.manager.init();
    await second.manager.add([fakeFile("b.mp4", 20 * MiB)]);
    const snapshot = await until(second.manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "part_rejected" } });
    expect(second.clock.sleeps).toEqual([]); // no retry
  });

  test("a part that keeps failing fails the file, which keeps its finished parts; Retry uploads only the rest", async () => {
    const { manager, transport, server } = setup({ options: { maxPartAttempts: 3, maxPartsPerFile: 1 } });
    let broken = true;
    transport.script = ({ part }) => (broken && part === 3 ? new PutError(500, "boom") : undefined);
    await manager.init();
    await manager.add([fakeFile("a.mp4", 60 * MiB)]); // 4 parts
    const failed = await until(manager, (s) => s.summary.failed === 1);
    expect(failed.items[0]?.error?.message).toContain("Part 3 failed 3 times");
    expect(failed.items[0]?.partsDone).toBe(2);
    const putsBefore = transport.puts.length;
    expect(putsBefore).toBe(2);

    broken = false;
    manager.retry(failed.items[0]?.id as string);
    await until(manager, doneCount(1));
    // The server reported parts 1 and 2 as held, so only 3 and 4 went over the wire again.
    expect(transport.puts.slice(putsBefore).map((p) => p.part)).toEqual([3, 4]);
    expect(server.get("ast_0001").status).toBe("completed");
    expect(server.count("create")).toBe(1);
  });

  test("a 403 means the URL expired: a new one is requested for that part and the PUT is repeated", async () => {
    const { manager, server, transport } = setup({ options: { maxFiles: 1, maxPartsPerFile: 1 } });
    // Just before part 2 is PUT, its URL (and the others') stops being valid, as if 15 minutes had passed.
    transport.script = ({ part, attempt }) => {
      if (part === 2 && attempt === 0) server.expireAllUrls();
      return undefined;
    };
    await manager.init();
    await manager.add([fakeFile("a.mp4", 40 * MiB)]); // 3 parts
    await until(manager, doneCount(1));
    expect(server.calls.filter((c) => c.startsWith("presign:"))).toEqual(["presign:ast_0001:2", "presign:ast_0001:3"]);
    expect(server.get("ast_0001").status).toBe("completed");
    expect(transport.puts.map((p) => p.part)).toEqual([1, 2, 3]);
  });

  test("a URL close to the end of its 15 minutes is renewed before it is used", async () => {
    const { manager, server, transport, clock } = setup({ options: { maxPartsPerFile: 1, maxFiles: 1 } });
    await manager.init();
    transport.hold = ({ part }) => (part === 1 ? new Promise<void>((resolve) => setTimeout(resolve, 5)).then(() => clock.advance(14 * 60_000)) : undefined);
    await manager.add([fakeFile("a.mp4", 40 * MiB)]);
    await until(manager, doneCount(1));
    // Part 1 took "14 minutes": the URLs for parts 2 and 3 from the create call were too old, so new ones were requested.
    expect(server.calls.filter((c) => c.startsWith("presign:"))).toEqual(["presign:ast_0001:2,3"]);
  });

  test("a lost connection waits for the browser to be online without using up the part's tries", async () => {
    const server = new FakeServer();
    let onlineNow = false;
    const waiting: (() => void)[] = [];
    const transport = new FakeTransport(server);
    const manager = new UploadManager({
      api: server,
      transport,
      store: createMemoryStore(),
      workspace: "w",
      clock: virtualClock(),
      connectivity: {
        isOnline: () => onlineNow,
        waitOnline: () => (onlineNow ? Promise.resolve() : new Promise<void>((resolve) => waiting.push(resolve))),
      },
      options: { emitIntervalMs: 0, maxPartAttempts: 1 },
      newId: () => "loc",
    });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(waiting.length).toBeGreaterThan(0));
    expect(transport.puts).toHaveLength(0);
    onlineNow = true;
    for (const resume of waiting) resume();
    await until(manager, doneCount(1));
  });

  test("a connection that is down for minutes is waited out, not failed after a fixed number of tries", async () => {
    const { manager, transport, server, clock } = setup({ options: { maxPartAttempts: 3, maxApiAttempts: 2, networkGraceMs: 5 * 60_000 } });
    // Down for the first 12 pauses (a few minutes of virtual time), then back.
    transport.script = () => (clock.sleeps.length < 12 ? new PutError(0, "The connection to object storage failed.") : undefined);
    server.failNext("state", ...Array.from({ length: 10 }, () => new ApiError(0, "network_error", "no")));
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(clock.sleeps.length).toBeGreaterThanOrEqual(12); // far more than the 3 tries of maxPartAttempts
    expect(Math.max(...clock.sleeps)).toBeLessThanOrEqual(30_000);
  });

  test("but not forever: after networkGraceMs without an answer the file fails, and keeps what it has", async () => {
    const { manager, transport } = setup({ options: { networkGraceMs: 2 * 60_000 } });
    transport.script = () => new PutError(0, "The connection to object storage failed.");
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    const snapshot = await until(manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "part_failed" } });
  });

  test("the grace for a lost connection counts from the part's last progress, not from its first failure", async () => {
    const { manager, transport, clock } = setup({ options: { networkGraceMs: 60_000, maxPartAttempts: 2 } });
    transport.progressBeforeFailing = true; // every attempt sends a few bytes before the connection drops
    transport.script = ({ attempt }) => (attempt < 12 ? new PutError(0, "The connection to object storage failed.") : undefined);
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)].slice(0, 1));
    await until(manager, doneCount(1));
    // Far more than 60 s of virtual time and more than maxPartAttempts failures passed, but each attempt made progress.
    expect(clock.sleeps.reduce((a, b) => a + b, 0)).toBeGreaterThan(60_000);
  });

  test("API calls that hit a gateway error are tried again", async () => {
    const { manager, server } = setup();
    server.failNext("create", new ApiError(502, "unknown_error", "bad gateway"));
    server.failNext("state", new ApiError(0, "network_error", "no"));
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.count("create")).toBe(2);
  });

  test("a refusal that retrying cannot change fails the file with a message, and tells the app", async () => {
    const server = new FakeServer();
    server.failNext("create", new ApiError(402, "plan_required", "no plan"));
    const onApiError = vi.fn();
    const transport = new FakeTransport(server);
    const manager = new UploadManager({ api: server, transport, store: createMemoryStore(), workspace: "w", clock: virtualClock(), connectivity: online, options: { emitIntervalMs: 0 }, onApiError });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    const snapshot = await until(manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "plan_required", message: "Your plan does not include uploads." } });
    expect(onApiError).toHaveBeenCalledWith(expect.objectContaining({ status: 402 }));
  });
});

describe("the server's cap of 25 uploads", () => {
  test("a 429 on create puts the file back in the queue, which retries with backoff until a slot is free", async () => {
    const server = new FakeServer();
    server.maxActive = 0;
    server.onCreate = (attempt) => {
      if (attempt === 4) server.maxActive = 25;
    };
    const { manager, clock } = setup({ server });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB), fakeFile("b.mp4", 20 * MiB)]);
    const waiting = manager.getSnapshot();
    expect(waiting.items.every((i) => i.status === "queued" || i.status === "starting")).toBe(true);
    await until(manager, doneCount(2));
    expect(server.count("create")).toBeGreaterThanOrEqual(4);
    // 429 is not a failure; the pauses grow and are long enough not to hammer the API.
    expect(clock.sleeps.length).toBeGreaterThan(0);
    expect(clock.sleeps.every((ms) => ms >= 1000)).toBe(true);
    expect(Math.max(...clock.sleeps)).toBeGreaterThan(Math.min(...clock.sleeps));
  });

  test("while the cap is hit, a file that already has its upload still goes on", async () => {
    const server = new FakeServer();
    const { manager, transport } = setup({ server, options: { maxFiles: 2 } });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    server.maxActive = 0;
    server.failNext("presign"); // (no-op: nothing is queued)
    await manager.add([fakeFile("b.mp4", 20 * MiB)]);
    // b gets 429 and waits; nothing else is lost.
    await vi.waitFor(() => expect(server.count("create")).toBeGreaterThanOrEqual(2));
    server.maxActive = 25;
    await until(manager, doneCount(2));
    expect(transport.puts.filter((p) => p.assetId === "ast_0002")).toHaveLength(2);
  });
});

describe("cancel", () => {
  test("aborts the requests in flight, aborts the upload on the server and forgets the file", async () => {
    const { manager, server, transport, store } = setup();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    transport.hold = () => gate;
    await manager.init();
    await manager.add([fakeFile("a.mp4", 60 * MiB)]);
    await vi.waitFor(() => expect(transport.inflight).toBe(4));
    expect(store.records.size).toBe(1);
    const id = manager.getSnapshot().items[0]?.id as string;

    manager.cancel(id);
    await vi.waitFor(() => expect(transport.inflight).toBe(0));
    expect(manager.getSnapshot().items).toHaveLength(0);
    await vi.waitFor(() => expect(server.get("ast_0001").status).toBe("aborted"));
    expect(store.records.size).toBe(0);
    release();
    await Promise.resolve();
    expect(transport.puts).toHaveLength(0); // nothing completed after the cancel
  });

  test("a queued file is just removed, and nothing is asked of the server", async () => {
    const { manager, server } = setup({ options: { maxFiles: 1 } });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 200 * MiB), fakeFile("b.mp4", 20 * MiB)]);
    const queued = manager.getSnapshot().items.find((i) => i.name === "b.mp4");
    expect(queued?.status).toBe("queued");
    manager.cancel(queued?.id as string);
    await until(manager, doneCount(1));
    expect(server.calls.some((c) => c.startsWith("abort"))).toBe(false);
    expect(server.assets.size).toBe(1);
  });

  test("a cancel while the create call is in flight removes the asset that call makes", async () => {
    const { manager, server } = setup();
    const create = server.create.bind(server);
    let finishCreate!: () => void;
    server.create = async (request) => {
      await new Promise<void>((resolve) => {
        finishCreate = resolve;
      });
      return create(request);
    };
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(finishCreate).toBeTypeOf("function"));
    manager.cancel(manager.getSnapshot().items[0]?.id as string);
    finishCreate();
    await vi.waitFor(() => expect(server.get("ast_0001").status).toBe("aborted"));
  });
});

describe("resume after a reload", () => {
  test("the record survives, waits for the same file, asks the server which parts it holds and uploads only the rest", async () => {
    const first = setup({ options: { maxPartsPerFile: 1, maxFiles: 1 } });
    // Park the PUT of part 3: parts 1 and 2 are done when "the tab closes".
    first.transport.hold = ({ part }) => (part === 3 ? new Promise<void>(() => {}) : undefined);
    await first.manager.init();
    const file = fakeFile("big.mp4", 70 * MiB); // 5 parts
    await first.manager.add([file]);
    await vi.waitFor(() => expect(first.transport.puts).toHaveLength(2));
    first.manager.dispose(); // the page is gone

    const record = [...first.store.records.values()][0];
    expect(record).toMatchObject({ name: "big.mp4", size: 70 * MiB, assetId: "ast_0001", partSize: 16 * MiB, partCount: 5 });

    // A new page: the same server, the same IndexedDB.
    const second = setup({ server: first.server, store: first.store, options: { maxPartsPerFile: 1, maxFiles: 1 } });
    await second.manager.init();
    const waiting = second.manager.getSnapshot();
    expect(waiting.summary).toMatchObject({ needsFile: 1, active: 0 });
    expect(waiting.items[0]).toMatchObject({ status: "needs-file", assetId: "ast_0001", name: "big.mp4" });
    expect(second.transport.puts).toHaveLength(0);

    // A different file (another date) is a new upload, not the interrupted one.
    expect(await second.manager.add([fakeFile("big.mp4", 70 * MiB, { lastModified: 5 })])).toMatchObject({ added: 1, resumed: 0 });
    second.manager.cancel(second.manager.getSnapshot().items.find((i) => i.status !== "needs-file")?.id as string);

    // The same file (a new File object with the same name, size and date) resumes it.
    expect(await second.manager.add([fakeFile("big.mp4", 70 * MiB)])).toMatchObject({ added: 0, resumed: 1 });
    await until(second.manager, (s) => s.items.some((i) => i.assetId === "ast_0001" && i.status === "done"));
    expect(second.transport.puts.map((p) => p.part)).toEqual([3, 4, 5]);
    expect([...second.server.assets.values()].filter((a) => a.asset.filename === "big.mp4" && a.status === "completed")).toHaveLength(1); // not created again
    expect(second.server.get("ast_0001").status).toBe("completed");
    expect(second.server.get("ast_0001").parts.size).toBe(5);
  });

  test("a part that the server holds with the wrong size is uploaded again", async () => {
    const first = setup({ options: { maxFiles: 1 } });
    first.transport.hold = () => new Promise<void>(() => {});
    await first.manager.init();
    await first.manager.add([fakeFile("a.mp4", 40 * MiB)]);
    await vi.waitFor(() => expect(first.transport.inflight).toBe(3));
    first.manager.dispose();
    first.server.get("ast_0001").parts.set(1, { etag: '"x"', size: 5 }); // a truncated leftover
    first.server.get("ast_0001").parts.set(2, { etag: '"y"', size: 16 * MiB });

    const second = setup({ server: first.server, store: first.store });
    await second.manager.init();
    await second.manager.add([fakeFile("a.mp4", 40 * MiB)]);
    await until(second.manager, doneCount(1));
    expect(second.transport.puts.map((p) => p.part).sort()).toEqual([1, 3]);
  });

  test("an upload that finished on the server while the page was closed is just done", async () => {
    const first = setup({ options: { maxFiles: 1 } });
    first.transport.hold = () => new Promise<void>(() => {});
    await first.manager.init();
    await first.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(first.transport.inflight).toBe(2));
    first.manager.dispose();
    const entry = first.server.get("ast_0001");
    entry.status = "completed";
    entry.asset = { ...entry.asset, status: "ready" };

    const second = setup({ server: first.server, store: first.store });
    await second.manager.init();
    await second.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(second.manager, doneCount(1));
    expect(second.transport.puts).toHaveLength(0);
    expect(second.store.records.size).toBe(0);
  });

  test("an upload that expired or was aborted starts over with a new one", async () => {
    const first = setup({ options: { maxFiles: 1 } });
    first.transport.hold = () => new Promise<void>(() => {});
    await first.manager.init();
    await first.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(first.transport.inflight).toBe(2));
    first.manager.dispose();
    first.server.get("ast_0001").status = "expired";

    const second = setup({ server: first.server, store: first.store });
    await second.manager.init();
    await second.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(second.manager, doneCount(1));
    expect(second.server.get("ast_0002").status).toBe("completed");
    expect(second.transport.puts.map((p) => p.assetId)).toEqual(["ast_0002", "ast_0002"]);
    // The old upload was aborted on the server before the new one was created, so it does not count towards the cap.
    expect(second.server.get("ast_0001").status).toBe("aborted");
    expect(second.server.calls.indexOf("abort:ast_0001")).toBeLessThan(second.server.calls.indexOf("create", 1));
  });

  test("a create call whose answer was lost is found again instead of making a second upload", async () => {
    const first = setup();
    // The answer of create never reaches the page, and the page is closed.
    const real = first.server.create.bind(first.server);
    first.server.create = async (request) => {
      await real(request);
      return new Promise<never>(() => {}); // the answer never arrives
    };
    await first.manager.init();
    await first.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(first.server.assets.size).toBeGreaterThan(0));
    first.manager.dispose();
    first.server.create = real;
    const stored = [...first.store.records.values()][0];
    expect(stored?.createStartedAt).not.toBeNull();

    const second = setup({ server: first.server, store: first.store });
    await second.manager.init();
    await second.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(second.manager, doneCount(1));
    expect(first.server.assets.size).toBe(1); // no second upload was made
    expect(first.server.get("ast_0001").status).toBe("completed");
  });

  test("a stored queue is per workspace", async () => {
    const store = createMemoryStore();
    await store.put({
      id: "x",
      workspace: "user_1:org_OTHER",
      assetId: "ast_9",
      createStartedAt: null,
      name: "other.mp4",
      size: 10,
      lastModified: 1,
      type: "video/mp4",
      partSize: null,
      partCount: null,
      bytesDone: 0,
      error: null,
      createdAt: 1,
    });
    const { manager } = setup({ store });
    await manager.init();
    expect(manager.getSnapshot().items).toHaveLength(0);
  });

  test("the stored file handle gives the file back without asking, where the browser allows it", async () => {
    const store = createMemoryStore();
    const file = fakeFile("h.mp4", 20 * MiB);
    await store.put({
      id: "h",
      workspace: "user_1:org_1",
      assetId: null,
      createStartedAt: null,
      name: "h.mp4",
      size: 20 * MiB,
      lastModified: file.lastModified,
      type: "video/mp4",
      partSize: null,
      partCount: null,
      bytesDone: 0,
      error: null,
      createdAt: 1,
      handle: { queryPermission: async () => "granted", getFile: async () => file },
    });
    const { manager } = setup({ store });
    await manager.init();
    await until(manager, doneCount(1));
  });
});

describe("before it completes", () => {
  test("the parts are checked against the server's list; one that went missing is uploaded again", async () => {
    const { manager, server, transport } = setup({ options: { maxFiles: 1 } });
    let dropped = false;
    const state = server.state.bind(server);
    server.state = async (assetId) => {
      if (!dropped && transport.puts.length === 3) {
        dropped = true;
        server.get(assetId).parts.delete(2); // object storage lost part 2
      }
      return state(assetId);
    };
    await manager.init();
    await manager.add([fakeFile("a.mp4", 40 * MiB)]); // 3 parts
    await until(manager, doneCount(1));
    expect(transport.puts.map((p) => p.part).sort()).toEqual([1, 2, 2, 3]);
  });

  test("a parts_mismatch answer makes it read the list again and try once more", async () => {
    const { manager, server } = setup();
    server.failNext("complete", new ApiError(422, "parts_mismatch", "a part changed"));
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.count("complete:")).toBe(2);
  });

  test("a stored object of the wrong size fails the file instead of retrying forever", async () => {
    const { manager, server } = setup();
    server.failNext("complete", new ApiError(422, "size_mismatch", "wrong size"));
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    const snapshot = await until(manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "size_mismatch" } });
  });

  test("a server plan that does not fit the file is refused before anything is sent", async () => {
    const { manager, server, transport } = setup();
    server.partSize = 16 * MiB;
    const create = server.create.bind(server);
    server.create = async (request) => {
      const response = await create(request);
      return { ...response, upload: { ...response.upload, part_count: response.upload.part_count + 1 } };
    };
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    const snapshot = await until(manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "bad_plan" } });
    expect(transport.puts).toHaveLength(0);
  });
});

describe("progress", () => {
  test("reports bytes, speed and time left while it runs", async () => {
    const { manager, transport, clock } = setup({ options: { maxFiles: 1 } });
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    transport.hold = () => gate;
    await manager.init();
    await manager.add([fakeFile("a.mp4", 32 * MiB)]);
    await vi.waitFor(() => expect(transport.inflight).toBe(2));
    clock.advance(1000);
    const running = manager.getSnapshot();
    // Each of the two parts reported half of its 16 MiB.
    expect(running.items[0]?.bytesDone).toBe(16 * MiB);
    expect(running.items[0]?.progress).toBeCloseTo(0.5);
    expect(running.summary.active).toBe(1);
    expect(running.items[0]?.status).toBe("uploading");
    release();
    const done = await until(manager, doneCount(1));
    expect(done.items[0]).toMatchObject({ progress: 1, bytesDone: 32 * MiB, speed: 0, etaSeconds: null });
  });
});

describe("a store that fails", () => {
  test("never stops an upload", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const broken: UploadStore = {
      list: async () => {
        throw new Error("no idb");
      },
      put: async () => {
        throw new Error("no idb");
      },
      delete: async () => {
        throw new Error("no idb");
      },
    };
    const server = new FakeServer();
    const manager = new UploadManager({ api: server, transport: new FakeTransport(server), store: broken, workspace: "w", clock: virtualClock(), connectivity: online, options: { emitIntervalMs: 0 } });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    warn.mockRestore();
  });
});

const NOW = Date.parse("2026-10-17T11:00:00Z");
const recordOf = (over: Partial<import("./store").UploadRecord> = {}): import("./store").UploadRecord => ({
  id: "r1",
  workspace: "user_1:org_1",
  assetId: null,
  createStartedAt: NOW,
  name: "a.mp4",
  size: 20 * MiB,
  lastModified: 1_700_000_000_000,
  type: "video/mp4",
  partSize: null,
  partCount: null,
  bytesDone: 0,
  error: null,
  createdAt: NOW,
  ...over,
});

/** Makes an asset on the server the way a create call whose answer was lost would have. */
async function lostCreate(server: FakeServer, over: { name?: string; size?: number } = {}) {
  await server.create({ filename: over.name ?? "a.mp4", size_bytes: over.size ?? 20 * MiB, content_type: "video/mp4" });
}

describe("a create call whose answer was lost", () => {
  test("an asset made after the call, matching name and size, and the only one, is adopted", async () => {
    const server = new FakeServer();
    server.createdAt = "2026-10-17T11:00:05Z";
    await lostCreate(server);
    const store = createMemoryStore();
    await store.put(recordOf());
    const { manager } = setup({ server, store, clock: virtualClock(NOW) });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.assets.size).toBe(1);
  });

  test("two candidates are ambiguous: a new upload is made, nothing is guessed", async () => {
    const server = new FakeServer();
    server.createdAt = "2026-10-17T11:00:05Z";
    await lostCreate(server);
    await lostCreate(server);
    const store = createMemoryStore();
    await store.put(recordOf());
    const { manager } = setup({ server, store, clock: virtualClock(NOW) });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.assets.size).toBe(3);
    expect(server.get("ast_0003").status).toBe("completed");
  });

  test("an asset made before the call was sent is someone else's", async () => {
    const server = new FakeServer();
    server.createdAt = "2026-10-17T10:00:00Z"; // an hour before the call
    await lostCreate(server);
    const store = createMemoryStore();
    await store.put(recordOf());
    const { manager } = setup({ server, store, clock: virtualClock(NOW) });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.assets.size).toBe(2);
    expect(server.get("ast_0001").status).toBe("active");
  });

  test("no look-up at all after a definite refusal, and the doubt is cleared", async () => {
    const { manager, server, store } = setup({ options: { maxApiAttempts: 1 } });
    server.failNext("create", new ApiError(400, "invalid_request", "bad"));
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    const snapshot = await until(manager, settled);
    expect(snapshot.items[0]).toMatchObject({ status: "failed", error: { code: "invalid_request" } });
    expect([...store.records.values()][0]?.createStartedAt).toBeNull();
    expect(server.count("recent")).toBe(0);
  });

  test("a look-up that fails does not lead to a second upload", async () => {
    const server = new FakeServer();
    server.createdAt = "2026-10-17T11:00:05Z";
    await lostCreate(server);
    server.failNext("recent", new ApiError(0, "network_error", "no"));
    const store = createMemoryStore();
    await store.put(recordOf());
    const { manager } = setup({ server, store, clock: virtualClock(NOW) });
    await manager.init();
    await manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await until(manager, doneCount(1));
    expect(server.assets.size).toBe(1);
  });

  test("cancelling while the create call is in doubt finds the asset and aborts it", async () => {
    const first = setup({ clock: virtualClock(NOW) });
    first.server.createdAt = "2026-10-17T11:00:05Z";
    const real = first.server.create.bind(first.server);
    first.server.create = async (request) => {
      await real(request);
      return new Promise<never>(() => {}); // the answer never arrives
    };
    await first.manager.init();
    await first.manager.add([fakeFile("a.mp4", 20 * MiB)]);
    await vi.waitFor(() => expect(first.server.assets.size).toBe(1));
    first.manager.cancel(first.manager.getSnapshot().items[0]?.id as string);
    await vi.waitFor(() => expect(first.server.get("ast_0001").status).toBe("aborted"));
  });
});

describe("adding a file again", () => {
  test("the file of a failed upload restarts it instead of being ignored as a duplicate", async () => {
    const { manager, transport, server } = setup({ options: { maxFiles: 1 } });
    transport.script = () => new PutError(400, "Object storage answered 400 (EntityTooLarge)", "EntityTooLarge");
    await manager.init();
    const file = fakeFile("a.mp4", 20 * MiB);
    await manager.add([file]);
    await until(manager, (s) => s.summary.failed === 1);
    transport.script = undefined;
    expect(await manager.add([fakeFile("a.mp4", 20 * MiB)])).toMatchObject({ added: 0, resumed: 1, duplicates: 0 });
    await until(manager, doneCount(1));
    expect(server.count("create")).toBe(1); // the same upload continued
  });
});
