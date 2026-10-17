import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { filterOf, LibraryGrid, sortAssets } from "./library-grid";
import { LibraryDemo, parseDemoState } from "./library-demo";
import { isLocalSamples, withLocalPreviews } from "./local-samples";
import { SAMPLE_ASSETS } from "./sample-data";
import type { Asset } from "./types";

afterEach(cleanup);

const titles = () => screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);

test("an empty library invites the first upload", () => {
  const onUpload = vi.fn();
  render(<LibraryGrid assets={[]} onUpload={onUpload} />);
  expect(screen.getByRole("heading", { name: "Upload your first clips" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Upload clips" }));
  expect(onUpload).toHaveBeenCalledTimes(1);
});

test("loading shows busy skeletons and no clips", () => {
  const { container } = render(<LibraryGrid assets={SAMPLE_ASSETS} loading />);
  expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
  expect(container.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(8);
  expect(screen.getByRole("status").textContent).toBe("Loading your clips");
  expect(screen.queryAllByRole("heading", { level: 3 })).toHaveLength(0);
});

test("the grid lists every clip, newest first by default", () => {
  render(<LibraryGrid assets={SAMPLE_ASSETS} />);
  expect(titles()).toHaveLength(SAMPLE_ASSETS.length);
  expect(titles()[0]).toBe("Back double biceps pose");
  expect(screen.getByText(`${SAMPLE_ASSETS.length} clips`)).toBeTruthy();
});

test("sort by duration, longest or shortest first", () => {
  render(<LibraryGrid assets={SAMPLE_ASSETS} />);
  const sort = screen.getByLabelText("Sort") as HTMLSelectElement;
  fireEvent.change(sort, { target: { value: "longest" } });
  expect(titles()[0]).toContain("Posing routine");
  fireEvent.change(sort, { target: { value: "shortest" } });
  expect(titles()[0]).toBe("Corrupt export from phone");
  fireEvent.change(sort, { target: { value: "newest" } });
  expect(titles()[0]).toBe("Back double biceps pose");
});

test("sortAssets is stable on ties and does not change its input", () => {
  const input = [...SAMPLE_ASSETS];
  const base = SAMPLE_ASSETS[0] as Asset;
  const a: Asset = { ...base, id: "ast_a", durationMs: 5000 };
  const b: Asset = { ...base, id: "ast_b", durationMs: 5000 };
  expect(sortAssets([b, a], "longest").map((x) => x.id)).toEqual(["ast_a", "ast_b"]);
  sortAssets(input, "shortest");
  expect(input).toEqual(SAMPLE_ASSETS);
});

test("the status filter narrows the grid; uploading and queued clips count as processing", () => {
  render(<LibraryGrid assets={SAMPLE_ASSETS} />);
  const filters = within(screen.getByRole("group", { name: "Filter by status" }));
  expect(filters.getByRole("button", { name: /^All/ }).getAttribute("aria-pressed")).toBe("true");

  fireEvent.click(filters.getByRole("button", { name: /^Failed/ }));
  expect(titles()).toEqual(["Corrupt export from phone"]);
  expect(screen.getByText("1 clip")).toBeTruthy();

  fireEvent.click(filters.getByRole("button", { name: /^Processing/ }));
  expect(titles().sort()).toEqual(["Cable fly, front angle", "Deadlift, heavy single", "Morning check-in", "Warm-up mobility"]);

  fireEvent.click(filters.getByRole("button", { name: /^Ready/ }));
  expect(titles().every((title) => !["Warm-up mobility", "Corrupt export from phone"].includes(title ?? ""))).toBe(true);
  expect(filterOf({ status: "uploaded" })).toBe("processing");
  expect(filterOf({ status: "ready" })).toBe("ready");
});

test("a filter with no clips left offers to show all", () => {
  const onlyReady = SAMPLE_ASSETS.filter((asset) => asset.status === "ready");
  const { rerender } = render(<LibraryGrid assets={SAMPLE_ASSETS} />);
  fireEvent.click(screen.getByRole("button", { name: /^Failed/ }));
  rerender(<LibraryGrid assets={onlyReady} />);
  expect(screen.getByText("No clips match")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Show all clips" }));
  expect(titles()).toHaveLength(onlyReady.length);
});

test("the grid passes open, delete and retry through", () => {
  const onOpen = vi.fn();
  render(<LibraryGrid assets={SAMPLE_ASSETS} onOpen={onOpen} onRetry={onOpen} />);
  fireEvent.click(screen.getByRole("button", { name: "Open Back double biceps pose" }));
  expect(onOpen).toHaveBeenLastCalledWith(expect.objectContaining({ title: "Back double biceps pose" }));
  fireEvent.click(screen.getByRole("button", { name: "Retry Corrupt export from phone" }));
  expect(onOpen).toHaveBeenLastCalledWith(expect.objectContaining({ status: "failed" }));
});

test("the demo reads its state and the local-samples switch from props", () => {
  expect(isLocalSamples("?samples=local")).toBe(true);
  expect(isLocalSamples("?samples=other")).toBe(false);
  expect(parseDemoState("loading")).toBe("loading");
  expect(parseDemoState("nope")).toBeUndefined();

  const { rerender } = render(<LibraryDemo />);
  expect(screen.queryByText(/Pick local videos/)).toBeNull();
  rerender(<LibraryDemo localSamples />);
  expect(screen.getByText(/Pick local videos/)).toBeTruthy();
  rerender(<LibraryDemo state="empty" />);
  expect(screen.getByText("Upload your first clips")).toBeTruthy();
});

test("newest first compares instants, not strings", () => {
  const base = SAMPLE_ASSETS[0] as Asset;
  // 10:00 at +02:00 is 08:00 UTC, earlier than 09:00 UTC although it sorts later as text.
  const earlier: Asset = { ...base, id: "ast_early", createdAt: "2026-10-12T10:00:00+02:00" };
  const later: Asset = { ...base, id: "ast_late", createdAt: "2026-10-12T09:00:00Z" };
  expect(sortAssets([earlier, later], "newest").map((x) => x.id)).toEqual(["ast_late", "ast_early"]);
});

test("local previews are given to ready clips only, round-robin", () => {
  const withUrls = withLocalPreviews(SAMPLE_ASSETS, ["blob:one", "blob:two"]);
  const ready = withUrls.filter((asset) => asset.status === "ready");
  expect(ready.map((asset) => asset.hoverPreviewUrl).slice(0, 3)).toEqual(["blob:one", "blob:two", "blob:one"]);
  expect(withUrls.filter((asset) => asset.status !== "ready").every((asset) => asset.hoverPreviewUrl === undefined)).toBe(true);
  expect(withLocalPreviews(SAMPLE_ASSETS, [])).toEqual(SAMPLE_ASSETS);
});
