import { describe, expect, test, vi } from "vitest";
import { ApiError, apiBaseUrl, createApiClient, fetchLibrarySummary, fetchMe, fetchWorkspacePlan } from "./client";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const ME = {
  user: { id: "usr_1", email: "ada@example.com" },
  workspace: { id: "wks_1", name: "Fit", role: "owner" },
  entitlements: { plan: "creator", status: "active", active: true, current_period_end: null },
};

/** A client whose fetch is `fetchMock`; openapi-fetch calls it with one Request. */
function clientWith(fetchMock: ReturnType<typeof vi.fn>, getToken: () => Promise<string | null> = async () => "tok_123") {
  return createApiClient({ getToken, baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch });
}

describe("the typed client", () => {
  test("sends the session token as a bearer header, to the API's address, without cookies", async () => {
    const fetchMock = vi.fn(async (_request: Request) => json(ME));
    const getToken = vi.fn(async () => "tok_123");
    const me = await fetchMe(clientWith(fetchMock, getToken));

    expect(me).toEqual(ME);
    expect(getToken).toHaveBeenCalledTimes(1); // a fresh token per request
    const request = fetchMock.mock.calls[0]?.[0] as Request;
    expect(request.url).toBe("http://api.test/v1/me");
    expect(request.method).toBe("GET");
    expect(request.headers.get("Authorization")).toBe("Bearer tok_123");
    expect(request.headers.get("Accept")).toBe("application/json");
    expect(request.credentials).toBe("omit");
    expect(request.mode).toBe("cors");
  });

  test("calls the other endpoints of the contract", async () => {
    const fetchMock = vi.fn(async (request: Request) =>
      json(request.url.endsWith("/v1/library/summary") ? { assets: 0, projects: 0 } : { ...ME.entitlements, source: "manual", limits: { ai_billing: "managed", exports: 15, storage_gb: 50 } }),
    );
    const client = clientWith(fetchMock);
    expect(await fetchWorkspacePlan(client)).toMatchObject({ plan: "creator", limits: { exports: 15 } });
    expect(await fetchLibrarySummary(client)).toEqual({ assets: 0, projects: 0 });
    expect(fetchMock.mock.calls.map(([request]) => new URL(request.url).pathname)).toEqual(["/v1/workspace/plan", "/v1/library/summary"]);
  });

  test("does not call the API when signed out", async () => {
    const fetchMock = vi.fn();
    await expect(fetchMe(clientWith(fetchMock, async () => null))).rejects.toMatchObject({ status: 401, code: "unauthenticated" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  test("passes the abort signal on", async () => {
    const fetchMock = vi.fn(async (request: Request) => {
      if (request.signal.aborted) throw new DOMException("aborted", "AbortError");
      return json(ME);
    });
    const controller = new AbortController();
    controller.abort();
    await expect(fetchMe(clientWith(fetchMock), controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });

  test("turns the API's error body into an ApiError with its code", async () => {
    const fetchMock = vi.fn(async () => json({ error: { code: "no_active_workspace", message: "choose a workspace" } }, 403));
    const error = await fetchMe(clientWith(fetchMock)).catch((e) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 403, code: "no_active_workspace", message: "choose a workspace" });
  });

  test("the paywall's 402 keeps its code", async () => {
    const fetchMock = vi.fn(async () => json({ error: { code: "plan_required", message: "this workspace has no active plan" } }, 402));
    await expect(fetchLibrarySummary(clientWith(fetchMock))).rejects.toMatchObject({ status: 402, code: "plan_required" });
  });

  test("copes with an answer that is not the API's error shape", async () => {
    const fetchMock = vi.fn(async () => new Response("<html>Bad gateway</html>", { status: 502 }));
    await expect(fetchMe(clientWith(fetchMock))).rejects.toMatchObject({ status: 502, code: "unknown_error" });
    const empty = vi.fn(async () => new Response(null, { status: 500 }));
    await expect(fetchMe(clientWith(empty))).rejects.toMatchObject({ status: 500, code: "unknown_error" });
  });

  test("reports an unreachable server as a network error, and never repeats the token", async () => {
    const fetchMock = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const error = await fetchMe(clientWith(fetchMock, async () => "secret-token")).catch((e) => e);
    expect(error).toMatchObject({ status: 0, code: "network_error" });
    expect(String(error.message)).not.toContain("secret-token");
  });
});

describe("apiBaseUrl", () => {
  test("is VITE_API_BASE_URL without a trailing slash", () => {
    vi.stubEnv("VITE_API_BASE_URL", "http://localhost:8080/");
    expect(apiBaseUrl()).toBe("http://localhost:8080");
  });

  test("fails loudly when it is not set", () => {
    vi.stubEnv("VITE_API_BASE_URL", "");
    expect(() => apiBaseUrl()).toThrow(/VITE_API_BASE_URL/);
  });
});
