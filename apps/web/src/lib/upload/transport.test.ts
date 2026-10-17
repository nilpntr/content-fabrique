import { afterEach, describe, expect, test, vi } from "vitest";
import { createXhrTransport, PutError } from "./transport";

class FakeXhr {
  static last: FakeXhr;
  method = "";
  url = "";
  withCredentials = true;
  sent: unknown;
  aborted = false;
  status = 0;
  responseText = "";
  headers: Record<string, string> = {};
  upload: { onprogress: ((event: { loaded: number }) => void) | null; onloadstart: (() => void) | null } = { onprogress: null, onloadstart: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  onabort: (() => void) | null = null;
  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  send(body: unknown) {
    this.sent = body;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  getResponseHeader(name: string) {
    return this.headers[name.toLowerCase()] ?? null;
  }
  answer(status: number, headers: Record<string, string> = {}, text = "") {
    this.status = status;
    this.headers = headers;
    this.responseText = text;
    this.onload?.();
  }
}

const XHR = FakeXhr as unknown as typeof XMLHttpRequest;
const blob = new Blob([new Uint8Array(10)]);

afterEach(() => vi.useRealTimers());

describe("the XHR transport", () => {
  test("PUTs the slice as is, without credentials or headers, reports progress and returns the ETag", async () => {
    const transport = createXhrTransport({ XHR });
    const progress: number[] = [];
    const done = transport.put("https://s3.test/x?sig=1", blob, { signal: new AbortController().signal, onProgress: (n) => progress.push(n) });
    const xhr = FakeXhr.last;
    expect(xhr).toMatchObject({ method: "PUT", url: "https://s3.test/x?sig=1", withCredentials: false });
    expect(xhr.sent).toBe(blob);
    xhr.upload.onprogress?.({ loaded: 4 });
    xhr.answer(200, { etag: '"abc"' });
    expect(await done).toBe('"abc"');
    expect(progress).toEqual([4, 10]);
  });

  test("an error status becomes a PutError with the S3 code", async () => {
    const transport = createXhrTransport({ XHR });
    const done = transport.put("u", blob, { signal: new AbortController().signal, onProgress: () => {} });
    FakeXhr.last.answer(403, {}, "<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>");
    await expect(done).rejects.toMatchObject({ name: "PutError", status: 403, s3Code: "AccessDenied" });
  });

  test("a network error is status 0", async () => {
    const transport = createXhrTransport({ XHR });
    const done = transport.put("u", blob, { signal: new AbortController().signal, onProgress: () => {} });
    FakeXhr.last.onerror?.();
    await expect(done).rejects.toEqual(expect.objectContaining({ status: 0 }));
    await expect(done).rejects.toBeInstanceOf(PutError);
  });

  test("an abort cancels the request and rejects with the signal's reason", async () => {
    const transport = createXhrTransport({ XHR });
    const controller = new AbortController();
    const done = transport.put("u", blob, { signal: controller.signal, onProgress: () => {} });
    const reason = new Error("canceled");
    controller.abort(reason);
    await expect(done).rejects.toBe(reason);
    expect(FakeXhr.last.aborted).toBe(true);
  });

  test("a request with no progress for stallMs, while nothing else moves either, is given up", async () => {
    vi.useFakeTimers();
    const transport = createXhrTransport({ XHR, stallMs: 1000, now: () => Date.now() });
    const done = transport.put("u", blob, { signal: new AbortController().signal, onProgress: () => {} });
    const caught = done.catch((e) => e);
    await vi.advanceTimersByTimeAsync(1500);
    expect(await caught).toMatchObject({ status: 0, message: "The upload stalled." });
    expect(FakeXhr.last.aborted).toBe(true);
  });

  test("each request is judged by its own progress: traffic on another request does not save a stalled one", async () => {
    vi.useFakeTimers();
    const transport = createXhrTransport({ XHR, stallMs: 1000, now: () => Date.now() });
    const stalled = transport.put("stalled", blob, { signal: new AbortController().signal, onProgress: () => {} });
    const caught = stalled.catch((e) => e);
    const stalledXhr = FakeXhr.last;
    const sending = transport.put("sending", blob, { signal: new AbortController().signal, onProgress: () => {} });
    const sendingXhr = FakeXhr.last;
    for (let i = 0; i < 6; i++) {
      await vi.advanceTimersByTimeAsync(500);
      sendingXhr.upload.onprogress?.({ loaded: i });
    }
    expect(await caught).toMatchObject({ status: 0, message: "The upload stalled." });
    expect(stalledXhr.aborted).toBe(true);
    expect(sendingXhr.aborted).toBe(false);
    sendingXhr.answer(200);
    await sending;
  });

  test("a request that keeps making progress is never given up, however slow", async () => {
    vi.useFakeTimers();
    const transport = createXhrTransport({ XHR, stallMs: 1000, now: () => Date.now() });
    const done = transport.put("slow", blob, { signal: new AbortController().signal, onProgress: () => {} });
    const xhr = FakeXhr.last;
    xhr.upload.onloadstart?.();
    for (let i = 0; i < 10; i++) {
      await vi.advanceTimersByTimeAsync(800);
      xhr.upload.onprogress?.({ loaded: i });
    }
    expect(xhr.aborted).toBe(false);
    xhr.answer(200);
    await done;
  });
});
