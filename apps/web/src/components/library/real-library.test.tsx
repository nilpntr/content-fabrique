import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ApiAsset } from "@/lib/api/client";
import { pollDelay, pollSignature } from "@/lib/api/hooks";
import type { UploadItem, UploadSnapshot } from "@/lib/upload/manager";
import { type UploadCommands, UploadCommandsContext, UploadStateContext } from "@/lib/upload/provider";
import { RealLibrary, toLibraryAsset } from "./real-library";
import { useSpriteModel } from "./use-sprite-model";

const mutate = vi.fn();
const getClient = vi.fn();
vi.mock("@/lib/api/provider", () => ({ useApiClient: () => getClient }));
const notify = vi.fn();
vi.mock("@/components/ui/toast", () => ({ notify: (...args: unknown[]) => notify(...args) }));
vi.mock("@/lib/api/hooks", async (original) => ({ ...(await original<typeof import("@/lib/api/hooks")>()), useDeleteAsset: () => ({ mutate }) }));

afterEach(() => {
  cleanup();
  mutate.mockReset();
  notify.mockReset();
});

const asset = (id: string, over: Partial<ApiAsset> = {}): ApiAsset => ({
  id: id as ApiAsset["id"],
  kind: "video",
  status: "uploading",
  filename: `${id}.mp4`,
  content_type: "video/mp4",
  size_bytes: 1000,
  sha256: null,
  title: `Clip ${id}`,
  description: "",
  tags: [],
  failure_reason: null,
  ingest_progress: 0,
  ingest_stage: null,
  transcript_status: "none",
  created_at: "2026-10-17T10:00:00Z",
  updated_at: "2026-10-17T10:00:00Z",
  ...over,
});

function upload(over: Partial<UploadItem> = {}): UploadItem {
  return { id: "loc_1", assetId: "ast_1", name: "ast_1.mp4", size: 1000, status: "uploading", progress: 0.42, bytesDone: 420, speed: 0, etaSeconds: null, partsDone: 0, partCount: 1, error: null, note: null, ...over };
}

let queryClient = new QueryClient();

function renderLibrary(assets: ApiAsset[], items: UploadItem[], commands: Partial<UploadCommands> = {}) {
  queryClient = new QueryClient();
  const snapshot = { items, ready: true, summary: {} } as unknown as UploadSnapshot;
  const full: UploadCommands = { available: true, addFiles: vi.fn(), cancel: vi.fn(), retry: vi.fn(), retryAllFailed: vi.fn(), clearDone: vi.fn(), ...commands };
  render(
    <QueryClientProvider client={queryClient}>
      <UploadCommandsContext.Provider value={full}>
        <UploadStateContext.Provider value={{ subscribe: () => () => {}, getSnapshot: () => snapshot }}>
          <RealLibrary assets={assets} />
        </UploadStateContext.Provider>
      </UploadCommandsContext.Provider>
    </QueryClientProvider>,
  );
  return full;
}

async function deleteClip(title: string) {
  const trigger = screen.getByRole("button", { name: `Actions for ${title}` });
  await act(async () => {
    fireEvent.pointerDown(trigger, { button: 0, pointerType: "mouse" });
    fireEvent.mouseDown(trigger, { button: 0 });
    fireEvent.pointerUp(trigger, { button: 0, pointerType: "mouse" });
    fireEvent.mouseUp(trigger, { button: 0 });
    fireEvent.click(trigger, { button: 0 });
  });
  fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
  fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete clip" }));
}

test("deleting a clip that this browser is uploading stops the upload here and deletes the asset on the server", async () => {
  const commands = renderLibrary([asset("ast_1")], [upload()]);
  await deleteClip("Clip ast_1");
  expect(commands.cancel).toHaveBeenCalledWith("loc_1", { abortOnServer: false });
  expect(mutate).toHaveBeenCalledWith("ast_1", expect.objectContaining({ onError: expect.any(Function) }));
});

test("deleting another clip only deletes the asset", async () => {
  const commands = renderLibrary([asset("ast_2", { status: "ready" })], [upload()]);
  await deleteClip("Clip ast_2");
  expect(commands.cancel).not.toHaveBeenCalled();
  expect(mutate).toHaveBeenCalledWith("ast_2", expect.anything());
});

test("a failed delete is reported in a toast", async () => {
  mutate.mockImplementation((_id: string, options: { onError: () => void }) => options.onError());
  renderLibrary([asset("ast_2", { status: "ready" })], []);
  await deleteClip("Clip ast_2");
  expect(notify).toHaveBeenCalledWith("The clip could not be deleted", expect.objectContaining({ type: "error" }));
});

test("an uploading asset shows this browser's live progress; a stopped upload shows as failed and can be retried", () => {
  expect(toLibraryAsset(asset("ast_1"), { id: "l", assetId: "ast_1", status: "uploading", percent: 42 })).toMatchObject({ status: "uploading", progress: 42, retryable: false });
  expect(toLibraryAsset(asset("ast_1"), { id: "l", assetId: "ast_1", status: "failed", percent: 42 })).toMatchObject({ status: "failed", progress: undefined, retryable: true });
  expect(toLibraryAsset(asset("ast_1"))).toMatchObject({ status: "uploading", progress: undefined });
});

test("only uploaded and processing assets that this browser does not own are polled, with a growing delay", () => {
  const list = [asset("a", { status: "uploading" }), asset("b", { status: "uploaded" }), asset("c", { status: "processing" }), asset("d", { status: "ready" }), asset("e", { status: "processing" })];
  expect(pollSignature(list, (id) => id === "e")).toBe("b:uploaded:2026-10-17T10:00:00Z,c:processing:2026-10-17T10:00:00Z");
  expect(pollSignature([list[0] as ApiAsset, list[3] as ApiAsset], () => false)).toBe("");
  expect([0, 1, 2, 3, 4, 5, 9].map(pollDelay)).toEqual([3000, 6000, 12000, 24000, 30000, 30000, 30000]);
});

const file = (url: string, extra: Record<string, unknown> = {}) => ({ url, content_type: "x", size_bytes: 1, ...extra });
const READY = (id: string, over: Partial<ApiAsset> = {}): ApiAsset =>
  asset(id, {
    status: "ready",
    ingest_progress: 100,
    media: { duration_ms: 12_500, width: 1080, height: 1920, fps: 30, rotation: 90, video_codec: "h264", audio_codec: "aac", has_audio: true, hdr: false },
    derivatives: {
      poster: file(`https://s3.test/${id}/poster.jpg?sig=1`),
      hover_preview: file(`https://s3.test/${id}/hover.mp4?sig=1`),
      sprite: file(`https://s3.test/${id}/sprite.jpg?sig=1`),
      sprite_vtt: file(`https://s3.test/${id}/sprite.vtt?sig=1`),
    },
    derivative_urls_expire_at: "2026-10-17T10:30:00Z",
    ...over,
  });

test("a ready clip gets its duration, upright size, poster, hover preview and scrub sprite from the API", async () => {
  const loadVtt = vi.fn(async () => "WEBVTT");
  const mapped = toLibraryAsset(READY("ast_9"), undefined, loadVtt);
  expect(mapped).toMatchObject({
    status: "ready",
    durationMs: 12_500,
    width: 1080,
    height: 1920, // portrait: the probe already applied the rotation
    posterUrl: "https://s3.test/ast_9/poster.jpg?sig=1",
    hoverPreviewUrl: "https://s3.test/ast_9/hover.mp4?sig=1",
    progress: undefined,
  });
  expect(mapped.sprite).toMatchObject({ url: "https://s3.test/ast_9/sprite.jpg?sig=1", vttKey: "ast_9:2026-10-17T10:30:00Z" });
  // The cues are fetched through the API (with the token), not from the stored track.
  await mapped.sprite?.loadVtt?.();
  expect(loadVtt).toHaveBeenCalledWith("ast_9");
});

test("a clip being processed shows the ingest's progress and stage; a ready clip without media is a placeholder", () => {
  expect(toLibraryAsset(asset("ast_1", { status: "processing", ingest_progress: 45, ingest_stage: "proxy" }))).toMatchObject({ status: "processing", progress: 45, stage: "Making the preview video" });
  expect(toLibraryAsset(asset("ast_1", { status: "uploaded" }))).toMatchObject({ status: "uploaded", progress: undefined, stage: undefined });
  expect(toLibraryAsset(READY("ast_2", { media: { duration_ms: 0, width: null, height: null, fps: null, rotation: 0, video_codec: null, audio_codec: "aac", has_audio: true, hdr: false }, derivatives: {} }))).toMatchObject({ durationMs: 0, width: 16, height: 9, posterUrl: undefined, sprite: undefined });
});

test("a clip that failed says why, and is not retryable from here", () => {
  expect(toLibraryAsset(asset("ast_1", { status: "failed", failure_reason: "corrupt_media" }))).toMatchObject({ status: "failed", failureMessage: "The file is damaged or was cut off. Try exporting it again.", retryable: false });
  expect(toLibraryAsset(asset("ast_1", { status: "failed", failure_reason: "something_new" })).failureMessage).toBe("This clip could not be added.");
});

test("a poster that does not load (an expired URL) fetches the list again, at most every few seconds", async () => {
  const invalidate = vi.spyOn(QueryClient.prototype, "invalidateQueries").mockResolvedValue();
  renderLibrary([READY("ast_9"), READY("ast_8")], []);
  for (const poster of document.querySelectorAll("img")) fireEvent.error(poster);
  expect(invalidate).toHaveBeenCalledTimes(1);
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["assets"] });
  invalidate.mockRestore();
});

test("the list is fetched again shortly before the URLs expire", () => {
  vi.useFakeTimers();
  vi.setSystemTime(Date.parse("2026-10-17T10:00:00Z"));
  const invalidate = vi.spyOn(QueryClient.prototype, "invalidateQueries").mockResolvedValue();
  renderLibrary([READY("ast_9")], []);
  vi.advanceTimersByTime(28 * 60_000);
  expect(invalidate).not.toHaveBeenCalled();
  vi.advanceTimersByTime(2 * 60_000);
  expect(invalidate).toHaveBeenCalledTimes(1);
  invalidate.mockRestore();
  vi.useRealTimers();
});

test("the scrub track is fetched through the API only once the pointer is on the card, and its cues give the frames", async () => {
  const vtt = "WEBVTT\n\n00:00:00.000 --> 00:00:05.000\nhttps://s3.test/sprite.jpg?X-Amz=1#xywh=0,0,160,90\n\n00:00:05.000 --> 00:00:10.000\nhttps://s3.test/sprite.jpg?X-Amz=1#xywh=160,0,160,90\n";
  const loadVtt = vi.fn(async () => vtt);
  const sprite = toLibraryAsset(READY("ast_7"), undefined, loadVtt).sprite;
  const { result, rerender } = renderHook(({ enabled }) => useSpriteModel(sprite, 10_000, enabled), { initialProps: { enabled: false } });
  expect(loadVtt).not.toHaveBeenCalled();
  expect(result.current).toBeNull();
  rerender({ enabled: true });
  await waitFor(() => expect(result.current).not.toBeNull());
  expect(loadVtt).toHaveBeenCalledTimes(1);
  expect(result.current?.at(0.9).tile).toMatchObject({ x: 160, w: 160 });
});
