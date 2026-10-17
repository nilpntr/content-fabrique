import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import { getRouter } from "@/router";

let root: Root | undefined;
let finishExit: () => void = () => {};

// jsdom has no Web Animations, so a popup would unmount at once. Give it one animation that ends when the test says so.
beforeEach(() => {
  const exit = new Promise<void>((resolve) => {
    finishExit = resolve;
  });
  Object.defineProperty(Element.prototype, "getAnimations", { configurable: true, value: () => [{ finished: exit }] });
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  Reflect.deleteProperty(Element.prototype, "getAnimations");
  cleanup();
});

async function openPalette() {
  const router = getRouter(createMemoryHistory({ initialEntries: ["/reports"] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  await screen.findByRole("heading", { name: "Reports" });
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  return (await screen.findByPlaceholderText("Search pages and actions…")) as HTMLInputElement;
}

const options = () => screen.queryAllByRole("option").map((option) => option.textContent);

test("the list keeps its filter while the palette fades out, and is reset once it is gone", async () => {
  const input = await openPalette();
  expect(options()).toHaveLength(6);

  fireEvent.change(input, { target: { value: "settings" } });
  await waitFor(() => expect(options()).toEqual(["Go to Settings"]));

  fireEvent.keyDown(input, { key: "Escape" });
  await waitFor(() => expect(document.querySelector("[data-ending-style]")).toBeTruthy());
  // Mid exit animation: the full list must not come back.
  expect(options()).toEqual(["Go to Settings"]);

  await act(async () => finishExit());
  await waitFor(() => expect(screen.queryByPlaceholderText("Search pages and actions…")).toBeNull());

  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  const reopened = (await screen.findByPlaceholderText("Search pages and actions…")) as HTMLInputElement;
  expect(reopened.value).toBe("");
  expect(within(document.body).queryAllByRole("option")).toHaveLength(6);
});

test("reopening during the exit animation starts with an empty query", async () => {
  const input = await openPalette();
  fireEvent.change(input, { target: { value: "settings" } });
  await waitFor(() => expect(options()).toEqual(["Go to Settings"]));

  fireEvent.keyDown(input, { key: "Escape" });
  await waitFor(() => expect(document.querySelector("[data-ending-style]")).toBeTruthy());
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });

  await waitFor(() => expect(options()).toHaveLength(6));
  expect((screen.getByPlaceholderText("Search pages and actions…") as HTMLInputElement).value).toBe("");
});
