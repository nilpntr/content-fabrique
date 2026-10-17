import { afterEach, beforeEach, vi } from "vitest";
import { clerkMock, installWindowClerk, ME } from "./test-clerk";

// jsdom has no matchMedia; the theme and mobile hooks read it.
window.matchMedia ??= (query: string) =>
  ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList;

// No Clerk in tests: the components are stubs and the hooks answer from `clerkMock`.
vi.mock("@clerk/tanstack-react-start", async () => await import("./test-clerk"));
// The route guard asks the server for the session; there is no Start server here, so the same answer comes from the mock.
vi.mock("@/lib/session", async () => {
  const { clerkMock } = await import("./test-clerk");
  return {
    fetchSession: async () => {
      clerkMock.serverSessionCalls++;
      return { ...(clerkMock.serverSession ?? clerkMock.session) };
    },
  };
});

installWindowClerk();

// No network either: the API answers /v1/me, and a test that wants something else stubs fetch itself.
beforeEach(() => {
  vi.stubEnv("VITE_API_BASE_URL", "http://api.test");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(ME), { status: 200, headers: { "Content-Type": "application/json" } })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  clerkMock.reset();
});
