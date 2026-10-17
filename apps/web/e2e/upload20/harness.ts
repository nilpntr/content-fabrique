// The browser side of the 20-file proof (run.mjs bundles this with `bun build`). It drives the real upload manager of
// apps/web/src/lib/upload with the real API client, XHR transport and IndexedDB store; only the page around it is
// missing (no Clerk: the token is a locally signed test token). Nothing here is shipped.
import { createApiClient } from "@/lib/api/client";
import { createUploadApi } from "@/lib/upload/api";
import { UploadManager } from "@/lib/upload/manager";
import { createUploadStore } from "@/lib/upload/store";
import { createXhrTransport } from "@/lib/upload/transport";

interface Config {
  api: string;
  token: string;
}
const cfg = (window as unknown as { __CFG__: Config }).__CFG__;

const stats = { putStarted: 0, putOk: 0, putFailed: 0, bytesOk: 0, inflight: 0, peakInflight: 0, failures: {} as Record<string, number> };
const base = createXhrTransport();
const transport = {
  async put(url: string, body: Blob, options: Parameters<typeof base.put>[2]) {
    stats.putStarted++;
    stats.inflight++;
    stats.peakInflight = Math.max(stats.peakInflight, stats.inflight);
    try {
      const etag = await base.put(url, body, options);
      stats.putOk++;
      stats.bytesOk += body.size;
      return etag;
    } catch (error) {
      stats.putFailed++;
      const key = error instanceof Error ? error.message : String(error);
      stats.failures[key] = (stats.failures[key] ?? 0) + 1;
      throw error;
    } finally {
      stats.inflight--;
    }
  },
};

const client = createApiClient({ getToken: async () => cfg.token, baseUrl: cfg.api });
const manager = new UploadManager({ api: createUploadApi(() => client), transport, store: createUploadStore(), workspace: "e2e:org_ada" });

(window as unknown as { cf: unknown }).cf = { manager, stats, snapshot: () => manager.getSnapshot() };
const input = document.getElementById("files") as HTMLInputElement;
input.addEventListener("change", () => {
  const files = [...(input.files ?? [])];
  void manager.add(files).then((result) => {
    (window as unknown as { lastAdd: unknown }).lastAdd = { ...result, files: files.length };
  });
});
void manager.init().then(() => {
  (window as unknown as { cfReady: boolean }).cfReady = true;
});
