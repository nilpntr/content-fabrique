/**
 * The PUT of one part to its presigned URL. `fetch` cannot report the progress of an upload, so this is an XMLHttpRequest.
 * The body is a `Blob` slice of the file, so the browser reads it from disk as it sends (nothing is held in memory) and
 * sets the Content-Length the URL was signed for. No other header is sent, none is signed.
 */

/** A part's PUT that did not succeed. `status` is the HTTP status, or 0 when there was no answer (network, stall). */
export class PutError extends Error {
  readonly status: number;
  /** The S3 error code in the body (`AccessDenied`, `SignatureDoesNotMatch`...), when there was one. */
  readonly s3Code: string | null;
  constructor(status: number, message: string, s3Code: string | null = null) {
    super(message);
    this.name = "PutError";
    this.status = status;
    this.s3Code = s3Code;
  }
}

export interface PutOptions {
  signal: AbortSignal;
  /** Bytes of the body sent so far. */
  onProgress(loaded: number): void;
}

export interface PartTransport {
  /** Resolves with the part's ETag (null when the browser may not read it). */
  put(url: string, body: Blob, options: PutOptions): Promise<string | null>;
}

export interface XhrTransportOptions {
  /** A request is given up (and retried) after this long without any upload progress anywhere. Default 60 s. */
  stallMs?: number;
  /** For tests. */
  XHR?: typeof XMLHttpRequest;
  now?: () => number;
}

/**
 * Stall detection looks at the activity of all requests together, not only the request's own. Over HTTP/1.1 a browser
 * sends six requests per host at a time and keeps the others waiting without a single event, which is not a stall.
 */
export function createXhrTransport(options: XhrTransportOptions = {}): PartTransport {
  const stallMs = options.stallMs ?? 60_000;
  const now = options.now ?? (() => Date.now());
  let lastActivity = now();
  return {
    put(url, body, { signal, onProgress }) {
      const XHR = options.XHR ?? XMLHttpRequest;
      return new Promise<string | null>((resolve, reject) => {
        if (signal.aborted) return reject(signal.reason);
        const xhr = new XHR();
        let watchdog: ReturnType<typeof setInterval> | undefined;
        let own = now();
        lastActivity = own;
        const finish = () => {
          clearInterval(watchdog);
          signal.removeEventListener("abort", onAbort);
        };
        const onAbort = () => {
          finish();
          xhr.abort();
          reject(signal.reason);
        };
        signal.addEventListener("abort", onAbort, { once: true });

        xhr.open("PUT", url);
        xhr.withCredentials = false;
        xhr.upload.onprogress = (event) => {
          own = lastActivity = now();
          onProgress(event.loaded);
        };
        xhr.onload = () => {
          finish();
          if (xhr.status >= 200 && xhr.status < 300) {
            onProgress(body.size);
            return resolve(xhr.getResponseHeader("ETag"));
          }
          const code = /<Code>([^<]{1,64})<\/Code>/.exec(xhr.responseText ?? "")?.[1] ?? null;
          reject(new PutError(xhr.status, `Object storage answered ${xhr.status}${code ? ` (${code})` : ""}.`, code));
        };
        xhr.onerror = () => {
          finish();
          reject(new PutError(0, "The connection to object storage failed."));
        };
        xhr.ontimeout = xhr.onerror;
        // An abort that the page did not ask for (the browser gave up the request).
        xhr.onabort = () => {
          finish();
          reject(signal.aborted ? signal.reason : new PutError(0, "The upload was interrupted."));
        };
        watchdog = setInterval(
          () => {
            if (now() - Math.max(own, lastActivity) > stallMs) {
              finish();
              reject(new PutError(0, "The upload stalled.")); // before abort(), whose onabort would reject first
              xhr.abort();
            }
          },
          Math.min(5_000, Math.max(10, stallMs / 4)),
        );
        xhr.send(body);
      });
    },
  };
}
