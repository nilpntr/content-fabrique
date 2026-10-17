import { describe, expect, test, vi } from "vitest";
import { ApiError, apiBaseUrl, apiRequest, fetchMe } from "./api";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const ME = { user: { id: "usr_1", email: "ada@example.com" }, workspace: { id: "wks_1", name: "Fit", role: "owner" } };

describe("apiRequest", () => {
  test("sends the session token as a bearer header, to the API's address, without cookies", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => json(ME));
    const getToken = vi.fn(async () => "tok_123");
    const me = await fetchMe({ getToken, baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch });

    expect(me).toEqual(ME);
    expect(getToken).toHaveBeenCalledTimes(1); // a fresh token per request
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("http://api.test/v1/me");
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer tok_123");
    expect(init?.method).toBe("GET");
    expect(init?.credentials).toBe("omit");
    expect(init?.mode).toBe("cors");
  });

  test("sends a JSON body with its content type", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => json({ ok: true }));
    await apiRequest("/v1/things", {
      getToken: async () => "t",
      method: "POST",
      body: { a: 1 },
      baseUrl: "http://api.test",
      fetch: fetchMock as unknown as typeof fetch,
    });
    const init = fetchMock.mock.calls[0]?.[1];
    expect(init?.body).toBe('{"a":1}');
    expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
  });

  test("does not call the API when signed out", async () => {
    const fetchMock = vi.fn();
    await expect(
      apiRequest("/v1/me", { getToken: async () => null, baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch }),
    ).rejects.toMatchObject({ status: 401, code: "unauthenticated" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  test("turns the API's error body into an ApiError with its code", async () => {
    const fetchMock = vi.fn(async () => json({ error: { code: "no_active_workspace", message: "choose a workspace" } }, 403));
    const error = await fetchMe({ getToken: async () => "t", baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch }).catch(
      (e) => e,
    );
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 403, code: "no_active_workspace", message: "choose a workspace" });
  });

  test("copes with an answer that is not the API's error shape", async () => {
    const fetchMock = vi.fn(async () => new Response("<html>Bad gateway</html>", { status: 502 }));
    await expect(
      fetchMe({ getToken: async () => "t", baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch }),
    ).rejects.toMatchObject({ status: 502, code: "unknown_error" });
  });

  test("reports an unreachable server as a network error, and never repeats the token", async () => {
    const fetchMock = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const error = await fetchMe({ getToken: async () => "secret-token", baseUrl: "http://api.test", fetch: fetchMock as unknown as typeof fetch }).catch(
      (e) => e,
    );
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
