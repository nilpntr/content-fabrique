import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import { getRouter } from "@/router";
import { sampleFacets } from "@/lib/sample-facets";
import { ME } from "@/test-clerk";
import { apiAsset } from "@/test-fixtures";

let root: Root | undefined;
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const CLIPS = [
  apiAsset(1, { title: "Seated cable row, back view", title_source: "quicklook", exercise: "seated cable row", facets: sampleFacets("type:lift", "body:back", "movement:row", "angle:back", "equipment:cable") }),
  apiAsset(2, { title: "Back pose", title_source: "quicklook", exercise: "back double biceps", facets: sampleFacets("type:pose", "body:back", "angle:back") }),
  apiAsset(3, { title: "Hammer curls", title_source: "quicklook", exercise: "hammer curl", facets: sampleFacets("type:lift", "type:slow_motion", "body:arms", "angle:front") }),
];

/** The library filters on the server: the list and the counts are asked with the labels picked, and the server says what matches. */
test("the library's filters ask the server for the clips with those labels, with counts per value", async () => {
  const asked: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const url = new URL(request.url);
      if (url.pathname === "/v1/me") return json(ME);
      if (url.pathname === "/v1/assets/facets") {
        const back = url.searchParams.get("body") === "back";
        return json({
          facets: [
            { facet: "body", label: "Body part", values: [{ value: "back", label: "Back", count: 2 }, { value: "arms", label: "Arms", count: 1 }] },
            { facet: "type", label: "Type", values: back ? [{ value: "lift", label: "Lifting", count: 1 }, { value: "pose", label: "Posing", count: 1 }] : [{ value: "lift", label: "Lifting", count: 2 }, { value: "pose", label: "Posing", count: 1 }, { value: "slow_motion", label: "Slow motion", count: 1 }] },
            { facet: "movement", label: "Movement", values: [{ value: "row", label: "Row", count: 1 }] },
          ],
        });
      }
      if (url.pathname === "/v1/assets") {
        asked.push(url.searchParams.toString());
        const want = ["body", "type", "angle"].flatMap((key) => (url.searchParams.get(key) ? [`${key}:${url.searchParams.get(key)}`] : []));
        // The pose was deleted on another device: the counts still offer it, the list has nothing.
        const items = url.searchParams.get("type") === "pose" ? [] : CLIPS.filter((clip) => want.every((w) => clip.facets.some((f) => `${f.facet}:${f.value}` === w)));
        return json({ items, next_cursor: null });
      }
      return json({ error: { code: "not_found", message: "no" } }, 404);
    }),
  );
  const router = getRouter(createMemoryHistory({ initialEntries: ["/library"] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));

  // The cards: the exercise as the heading and at most two labels.
  expect(await screen.findByText("Seated cable row")).toBeTruthy();
  expect(screen.getByText("Hammer curl")).toBeTruthy();
  expect(screen.getAllByText("Slow motion").length).toBeGreaterThan(0);

  // Body part and Type have values; Angle has none in this answer, so it is not offered. Movement is not one of the filters.
  const body = (await screen.findByRole("combobox", { name: "Body part" })) as HTMLSelectElement;
  expect([...body.options].map((o) => o.text)).toEqual(["Body part", "Back (2)", "Arms (1)"]);
  expect(screen.getByRole("combobox", { name: "Type" })).toBeTruthy();
  expect(screen.queryByRole("combobox", { name: "Angle" })).toBeNull();
  expect(screen.queryByRole("combobox", { name: "Movement" })).toBeNull();

  fireEvent.change(body, { target: { value: "back" } });
  await waitFor(() => expect(asked.some((q) => q.includes("body=back"))).toBe(true));
  await waitFor(() => expect(screen.queryByText("Hammer curl")).toBeNull());
  expect(screen.getByText("Seated cable row")).toBeTruthy();
  const type = screen.getByRole("combobox", { name: "Type" }) as HTMLSelectElement;
  await waitFor(() => expect([...type.options].map((o) => o.text)).toEqual(["Type", "Lifting (1)", "Posing (1)"]));

  // A combination nothing has (any more) says so, with a way back.
  fireEvent.change(type, { target: { value: "pose" } });
  expect(await screen.findByText("No clips match these filters")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Show all clips" }));
  expect(await screen.findByText("Hammer curl")).toBeTruthy();
  expect((screen.getByRole("combobox", { name: "Body part" }) as HTMLSelectElement).value).toBe("");
});
