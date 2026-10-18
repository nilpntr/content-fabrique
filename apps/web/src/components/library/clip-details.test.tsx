import { sampleFacets } from "@/lib/sample-facets";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { apiAsset } from "@/test-fixtures";
import { ClipDetails, renameMessage } from "./clip-details";
import { toLibraryAsset } from "./real-library";

afterEach(cleanup);

const asset = (over = {}) => toLibraryAsset(apiAsset(1, { title: "Seated cable row", description: "Controlled rows with a pause at the squeeze.", exercise: "seated cable row", facets: sampleFacets("type:lift", "body:back", "angle:side"), quick_look_status: "succeeded", ...over }));

test("an opened clip shows its loop, name, what the quick look wrote and its facts", () => {
  render(<ClipDetails asset={asset()} onClose={() => {}} onRename={async () => {}} />);
  expect(screen.getByRole("heading", { name: "Seated cable row" })).toBeTruthy();
  expect(screen.getByText("Controlled rows with a pause at the squeeze.")).toBeTruthy();
  expect(screen.getAllByRole("listitem").map((item) => item.textContent)).toEqual(["Lifting", "Back", "Side view"]);
  expect(screen.getByText("Seated cable row", { selector: "span" })).toBeTruthy(); // the exercise
  expect(screen.getByLabelText("Preview loop of the clip").getAttribute("src")).toBe("https://cdn.test/hover-1.mp4");
  expect(screen.getByText("1080×1920")).toBeTruthy();
});

test("a clip the quick look has not finished says it is coming; one it could not describe says so", () => {
  const { rerender } = render(<ClipDetails asset={asset({ description: "", quick_look_status: "pending" })} onClose={() => {}} onRename={async () => {}} />);
  expect(screen.getByText(/I'm watching this clip/)).toBeTruthy();
  rerender(<ClipDetails asset={asset({ description: "", quick_look_status: "failed" })} onClose={() => {}} onRename={async () => {}} />);
  expect(screen.getByText(/couldn't describe this clip/)).toBeTruthy();
  rerender(<ClipDetails asset={asset({ description: "", status: "processing" })} onClose={() => {}} onRename={async () => {}} />);
  expect(screen.getByText(/as soon as it's ready/)).toBeTruthy();
  // A ready clip that has no quick look status (an older upload) is not promised one.
  rerender(<ClipDetails asset={toLibraryAsset(apiAsset(1, { description: "" }))} onClose={() => {}} onRename={async () => {}} />);
  expect(screen.getByText("No description for this clip.")).toBeTruthy();
});

test("the name can be changed: the new name is sent trimmed, and the field closes", async () => {
  const onRename = vi.fn(async () => {});
  render(<ClipDetails asset={asset()} onClose={() => {}} onRename={onRename} />);
  fireEvent.click(screen.getByRole("button", { name: "Rename the clip" }));
  const field = screen.getByLabelText("Clip name") as HTMLInputElement;
  expect(field.value).toBe("Seated cable row");
  fireEvent.change(field, { target: { value: "  Cable row, heavy  " } });
  fireEvent.click(screen.getByRole("button", { name: "Save the name" }));
  await waitFor(() => expect(onRename).toHaveBeenCalledWith(expect.objectContaining({ title: "Seated cable row" }), "Cable row, heavy"));
  await waitFor(() => expect(screen.queryByLabelText("Clip name")).toBeNull());
});

test("a name the server refuses stays in the field with the reason; an unchanged or empty name is not sent", async () => {
  const onRename = vi.fn(async () => {
    throw new ApiError(403, "forbidden", "no");
  });
  render(<ClipDetails asset={asset()} onClose={() => {}} onRename={onRename} />);
  fireEvent.click(screen.getByRole("button", { name: "Rename the clip" }));
  const field = screen.getByLabelText("Clip name") as HTMLInputElement;

  fireEvent.change(field, { target: { value: "   " } });
  expect((screen.getByRole("button", { name: "Save the name" }) as HTMLButtonElement).disabled).toBe(true);

  fireEvent.change(field, { target: { value: "Seated cable row" } });
  fireEvent.click(screen.getByRole("button", { name: "Save the name" }));
  await waitFor(() => expect(screen.queryByLabelText("Clip name")).toBeNull());
  expect(onRename).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole("button", { name: "Rename the clip" }));
  fireEvent.change(screen.getByLabelText("Clip name"), { target: { value: "New name" } });
  fireEvent.click(screen.getByRole("button", { name: "Save the name" }));
  expect((await screen.findByRole("alert")).textContent).toMatch(/can't rename clips/);
  expect((screen.getByLabelText("Clip name") as HTMLInputElement).value).toBe("New name");
});

test("nothing is shown for no clip, and closing says so", () => {
  const onClose = vi.fn();
  const { rerender } = render(<ClipDetails asset={null} onClose={onClose} onRename={async () => {}} />);
  expect(screen.queryByRole("dialog")).toBeNull();
  rerender(<ClipDetails asset={asset()} onClose={onClose} onRename={async () => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalled();
});

test("rename failures are explained", () => {
  expect(renameMessage(new ApiError(400, "bad_request", ""))).toMatch(/1 to 255 characters/);
  expect(renameMessage(new ApiError(404, "not_found", ""))).toMatch(/gone/);
  expect(renameMessage(new ApiError(0, "network_error", ""))).toMatch(/could not be reached/);
  expect(renameMessage(new Error("x"))).toMatch(/could not be renamed/);
});
