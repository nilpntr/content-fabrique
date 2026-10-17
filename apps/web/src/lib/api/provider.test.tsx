import { act } from "@testing-library/react";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
import { clerkMock } from "@/test-clerk";
import type { ApiClient } from "./client";
import { ApiClientProvider, useApiClient } from "./provider";

function Grab({ into }: { into: ApiClient[]; render?: number }) {
  const getClient = useApiClient();
  into.push(getClient());
  return null;
}

test("every component, and every render, gets the same client", () => {
  vi.stubEnv("VITE_API_BASE_URL", "http://api.test");
  const seen: ApiClient[] = [];
  const root = createRoot(document.createElement("div"));
  const tree = (render: number) => (
    <ApiClientProvider>
      <Grab into={seen} render={render} />
      <Grab into={seen} render={render} />
    </ApiClientProvider>
  );
  act(() => root.render(tree(1)));
  // Clerk's getToken is a new function on every render in the mock; the client does not follow it.
  act(() => root.render(tree(2)));
  expect(seen.length).toBeGreaterThanOrEqual(4);
  expect(new Set(seen).size).toBe(1);
  act(() => root.unmount());
});

test("the one client asks Clerk for the current token at each request", async () => {
  vi.stubEnv("VITE_API_BASE_URL", "http://api.test");
  const seen: ApiClient[] = [];
  const root = createRoot(document.createElement("div"));
  act(() => root.render(<ApiClientProvider><Grab into={seen} /></ApiClientProvider>));
  const fetchMock = vi.fn(async (_request: Request) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);
  const client = seen[0];

  clerkMock.token = "first";
  await client?.GET("/v1/library/summary");
  clerkMock.token = "second";
  await client?.GET("/v1/library/summary");
  expect(fetchMock.mock.calls.map(([request]) => request.headers.get("Authorization"))).toEqual(["Bearer first", "Bearer second"]);
  act(() => root.unmount());
});

test("a hook outside the provider says what is missing", () => {
  const root = createRoot(document.createElement("div"));
  const error = vi.spyOn(console, "error").mockImplementation(() => {});
  expect(() => act(() => root.render(<Grab into={[]} />))).toThrow(/ApiClientProvider/);
  error.mockRestore();
});
