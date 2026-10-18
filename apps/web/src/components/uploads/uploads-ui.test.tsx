import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { getRouter } from "@/router";
import { ME } from "@/test-clerk";
import type { UploadItem, UploadSnapshot } from "@/lib/upload/manager";
import { type UploadCommands, UploadCommandsContext, UploadStateContext, useUploadSelector } from "@/lib/upload/provider";
import { ResumePrompt, summaryText, UploadsTray } from "./uploads-tray";
import { UploadButton } from "./upload-button";
import { UploadDropzone } from "./upload-dropzone";

const MiB = 1024 * 1024;

function item(over: Partial<UploadItem> = {}): UploadItem {
  return { id: "a", assetId: null, name: "clip.mp4", size: 100 * MiB, status: "uploading", progress: 0.5, bytesDone: 50 * MiB, speed: 5 * MiB, etaSeconds: 10, partsDone: 3, partCount: 7, error: null, note: null, ...over };
}

function snapshot(items: UploadItem[]): UploadSnapshot {
  const count = (...s: UploadItem["status"][]) => items.filter((i) => s.includes(i.status)).length;
  return {
    items,
    ready: true,
    summary: {
      total: items.length,
      active: count("starting", "uploading", "completing"),
      queued: count("queued"),
      needsFile: count("needs-file"),
      failed: count("failed"),
      done: count("done"),
      bytesTotal: items.reduce((n, i) => n + i.size, 0),
      bytesDone: items.reduce((n, i) => n + i.bytesDone, 0),
      speed: items.reduce((n, i) => n + i.speed, 0),
      etaSeconds: 10,
    },
  };
}

type Fake = UploadCommands & { snapshot: UploadSnapshot };

function api(items: UploadItem[], over: Partial<UploadCommands> = {}): Fake {
  return {
    snapshot: snapshot(items),
    available: true,
    addFiles: vi.fn(async () => ({ added: 1, resumed: 0, duplicates: 0, rejected: [] })),
    cancel: vi.fn(),
    retry: vi.fn(),
    retryAllFailed: vi.fn(),
    clearDone: vi.fn(),
    ...over,
  };
}

const withApi = (value: Fake, children: ReactNode) => (
  <UploadCommandsContext.Provider value={value}>
    <UploadStateContext.Provider value={{ subscribe: () => () => {}, getSnapshot: () => value.snapshot }}>{children}</UploadStateContext.Provider>
  </UploadCommandsContext.Provider>
);

afterEach(cleanup);

test("the tray lists every file with an accessible progress bar, speed and time left", () => {
  render(withApi(api([item(), item({ id: "b", name: "other.mp4", status: "queued", progress: 0, bytesDone: 0, speed: 0, etaSeconds: null })]), <UploadsTray />));
  const bar = screen.getByRole("progressbar", { name: "Upload of clip.mp4" });
  expect(bar.getAttribute("aria-valuenow")).toBe("50");
  expect(bar.getAttribute("aria-valuetext")).toBe("50%. 50.0 MB of 100 MB · 5.00 MB/s · 10 s left");
  expect(screen.getByRole("progressbar", { name: "Upload of other.mp4" }).getAttribute("aria-valuenow")).toBe("0");
  expect(screen.getByRole("button", { name: /Uploading 0 of 2 · 5.00 MB\/s · 10 s left/ })).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("0 of 2 uploads finished");
});

test("the tray is hidden without uploads and collapses from its header button", () => {
  const { container, rerender } = render(withApi(api([]), <UploadsTray />));
  expect(container.firstChild).toBeNull();
  rerender(withApi(api([item()]), <UploadsTray />));
  const header = screen.getByRole("button", { name: /Uploading/ });
  expect(header.getAttribute("aria-expanded")).toBe("true");
  fireEvent.click(header);
  expect(header.getAttribute("aria-expanded")).toBe("false");
  expect(screen.queryByRole("progressbar")).toBeNull();
});

test("a failed file shows why and offers Retry; any file can be cancelled", () => {
  const value = api([item({ id: "f", name: "bad.mp4", status: "failed", progress: 0.3, error: { code: "part_failed", message: "Part 3 failed 8 times: boom" } }), item({ id: "g", name: "bad2.mp4", status: "failed" })]);
  render(withApi(value, <UploadsTray />));
  expect(screen.getByText("Part 3 failed 8 times: boom")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry upload of bad.mp4" }));
  expect(value.retry).toHaveBeenCalledWith("f");
  fireEvent.click(screen.getByRole("button", { name: "Cancel upload of bad.mp4" }));
  expect(value.cancel).toHaveBeenCalledWith("f");
  fireEvent.click(screen.getByRole("button", { name: "Retry all failed" }));
  expect(value.retryAllFailed).toHaveBeenCalled();
});

test("finished files can be cleared", () => {
  const value = api([item({ id: "d", name: "done.mp4", status: "done", progress: 1, bytesDone: 100 * MiB, speed: 0, etaSeconds: null })]);
  render(withApi(value, <UploadsTray />));
  expect(screen.getByText("Uploaded")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear finished" }));
  expect(value.clearDone).toHaveBeenCalled();
  expect(summaryText(value.snapshot.summary)).toBe("1 upload finished");
});

test("after a reload the interrupted uploads ask for their files, with a way to choose them", () => {
  const value = api([item({ id: "n", name: "big.mp4", status: "needs-file", progress: 0.62, bytesDone: 62 * MiB, speed: 0, etaSeconds: null })]);
  render(withApi(value, <ResumePrompt />));
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toContain("1 upload was interrupted.");
  expect(within(alert).getByRole("button", { name: "Choose files" })).toBeTruthy();
  cleanup();
  render(withApi(value, <UploadsTray />));
  expect(screen.getByText("Choose this file again to continue (62% was uploaded)")).toBeTruthy();
  expect(screen.getByRole("button", { name: /1 upload needs its file/ })).toBeTruthy();
});

test("the upload button hands the chosen files to the queue, and can pick the same file twice", () => {
  const value = api([]);
  render(withApi(value, <UploadButton />));
  const input = screen.getByTestId("upload-input") as HTMLInputElement;
  const file = new File(["x"], "a.mp4", { type: "video/mp4" });
  fireEvent.change(input, { target: { files: [file] } });
  expect(value.addFiles).toHaveBeenCalledWith([file]);
  expect(input.accept).toContain("video/mp4");
  expect(input.multiple).toBe(true);
});

test("the upload button is disabled where there is no workspace to upload to", () => {
  render(withApi(api([], { available: false }), <UploadButton />));
  expect((screen.getByRole("button", { name: "Upload clips" }) as HTMLButtonElement).disabled).toBe(true);
});

test("dropping files on the zone adds them; dragging something else over it does nothing", async () => {
  const value = api([]);
  render(withApi(value, <UploadDropzone />));
  const zone = screen.getByRole("region", { name: "Upload clips" });
  const file = new File(["x"], "a.mp4", { type: "video/mp4" });
  const dataTransfer = { types: ["Files"], files: [file], items: [], dropEffect: "none" };

  fireEvent.dragEnter(zone, { dataTransfer });
  expect(zone.getAttribute("data-dragging")).toBe("true");
  expect(within(zone).getByText("Drop to upload")).toBeTruthy();
  fireEvent.drop(zone, { dataTransfer });
  await vi.waitFor(() => expect(value.addFiles).toHaveBeenCalledWith([{ file }]));
  expect(zone.getAttribute("data-dragging")).toBe("false");

  fireEvent.dragEnter(zone, { dataTransfer: { types: ["text/plain"], files: [], items: [] } });
  expect(zone.getAttribute("data-dragging")).toBe("false");
});

// ── The library on real assets ────────────────────────────────────────────

let root: Root | undefined;
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const ASSET = (id: string, over: Record<string, unknown> = {}) => ({
  id,
  kind: "video",
  status: "ready",
  filename: `${id}.mp4`,
  content_type: "video/mp4",
  size_bytes: 1000,
  sha256: null,
  title: `Real clip ${id}`,
  description: "",
  tags: [],
  failure_reason: null,
  ingest_progress: 0,
  ingest_stage: null,
  created_at: "2026-10-17T10:00:00Z",
  updated_at: "2026-10-17T10:00:00Z",
  ...over,
});

function renderLibrary(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
}

test("/library lists the workspace's real assets from the API, with no sample notice, and shows uploads in progress", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      if (new URL(request.url).pathname === "/v1/me") return json(ME);
      return new Response(JSON.stringify({ items: [ASSET("ast_1"), ASSET("ast_2", { status: "uploading", title: "Still uploading" })], next_cursor: null }), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
  renderLibrary("/library");
  expect(await screen.findByText("Real clip ast_1")).toBeTruthy();
  expect(screen.getByText("Still uploading")).toBeTruthy();
  expect(screen.queryByRole("note")).toBeNull();
  expect(screen.queryByText("Back double biceps pose")).toBeNull();
  expect(screen.getByText("Uploading")).toBeTruthy();
  // The upload zone and button are there.
  expect(screen.getByRole("region", { name: "Upload clips" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Upload clips" })).toBeTruthy();
});

test("?samples=local keeps the samples even when the API has assets", async () => {
  const fetchMock = vi.fn(async (request: Request) => (new URL(request.url).pathname === "/v1/me" ? json(ME) : json({ items: [ASSET("ast_1")], next_cursor: null })));
  vi.stubGlobal("fetch", fetchMock);
  renderLibrary("/library?samples=local");
  expect(await screen.findByText("Back double biceps pose")).toBeTruthy();
  expect(screen.queryByText("Real clip ast_1")).toBeNull();
  expect(fetchMock.mock.calls.every(([request]) => new URL(request.url).pathname !== "/v1/assets")).toBe(true);
});

test("when the API cannot list assets the library says so with a retry, and never shows made-up clips", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => (new URL(request.url).pathname === "/v1/me" ? json(ME) : json({ error: { code: "plan_required", message: "no plan" } }, 402))),
  );
  renderLibrary("/library");
  expect((await screen.findByRole("alert")).textContent).toContain("Your clips could not be loaded");
  expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  expect(screen.queryByText("Back double biceps pose")).toBeNull();
});

test("a component that selects part of the queue renders when that part changes, not on every progress tick", () => {
  let listener: (() => void) | undefined;
  let current = snapshot([item({ progress: 0.1, bytesDone: 10 * MiB })]);
  const store = {
    subscribe: (l: () => void) => {
      listener = l;
      return () => {};
    },
    getSnapshot: () => current,
  };
  let renders = 0;
  function Probe() {
    renders++;
    const status = useUploadSelector((s) => s.items[0]?.status);
    return <p>{status}</p>;
  }
  render(
    <UploadCommandsContext.Provider value={api([])}>
      <UploadStateContext.Provider value={store}>
        <Probe />
      </UploadStateContext.Provider>
    </UploadCommandsContext.Provider>,
  );
  const before = renders;
  for (let i = 2; i < 8; i++) {
    current = snapshot([item({ progress: i / 10, bytesDone: i * 10 * MiB })]);
    act(() => listener?.());
  }
  expect(renders).toBe(before); // six progress ticks, no render
  current = snapshot([item({ status: "failed" })]);
  act(() => listener?.());
  expect(screen.getByText("failed")).toBeTruthy();
});
