import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ApiAsset } from "@/lib/api/client";
import { pollDelay, pollSignature } from "@/lib/api/hooks";
import type { UploadItem, UploadSnapshot } from "@/lib/upload/manager";
import { type UploadCommands, UploadCommandsContext, UploadStateContext } from "@/lib/upload/provider";
import { RealLibrary, toLibraryAsset } from "./real-library";

const mutate = vi.fn();
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
  created_at: "2026-10-17T10:00:00Z",
  updated_at: "2026-10-17T10:00:00Z",
  ...over,
});

function upload(over: Partial<UploadItem> = {}): UploadItem {
  return { id: "loc_1", assetId: "ast_1", name: "ast_1.mp4", size: 1000, status: "uploading", progress: 0.42, bytesDone: 420, speed: 0, etaSeconds: null, partsDone: 0, partCount: 1, error: null, note: null, ...over };
}

function renderLibrary(assets: ApiAsset[], items: UploadItem[], commands: Partial<UploadCommands> = {}) {
  const snapshot = { items, ready: true, summary: {} } as unknown as UploadSnapshot;
  const full: UploadCommands = { available: true, addFiles: vi.fn(), cancel: vi.fn(), retry: vi.fn(), retryAllFailed: vi.fn(), clearDone: vi.fn(), ...commands };
  render(
    <UploadCommandsContext.Provider value={full}>
      <UploadStateContext.Provider value={{ subscribe: () => () => {}, getSnapshot: () => snapshot }}>
        <RealLibrary assets={assets} />
      </UploadStateContext.Provider>
    </UploadCommandsContext.Provider>,
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
