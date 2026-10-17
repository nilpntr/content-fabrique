import { describe, expect, test } from "vitest";
import { readClientSession, sessionFromClerk } from "./client-session";

describe("sessionFromClerk", () => {
  test("a signed-in user with a workspace", () => {
    expect(sessionFromClerk({ loaded: true, user: { id: "user_1" }, session: { status: "active" }, organization: { id: "org_1" } })).toEqual({
      isAuthenticated: true,
      userId: "user_1",
      orgId: "org_1",
    });
  });

  test("a signed-in user without a workspace", () => {
    expect(sessionFromClerk({ loaded: true, user: { id: "user_1" }, session: { status: "active" }, organization: null }).orgId).toBeNull();
  });

  test("signed out", () => {
    expect(sessionFromClerk({ loaded: true, user: null, session: null, organization: null })).toEqual({ isAuthenticated: false, userId: null, orgId: null });
  });

  test("a pending session (an unfinished task) is signed out, as on the server", () => {
    expect(
      sessionFromClerk({ loaded: true, user: { id: "user_1" }, session: { status: "pending" }, organization: { id: "org_1" } }),
    ).toEqual({ isAuthenticated: false, userId: null, orgId: null });
  });
});

describe("readClientSession", () => {
  test("gives up when Clerk has not loaded in time", async () => {
    const clerk = (window as unknown as { Clerk?: unknown }).Clerk;
    Object.defineProperty(window, "Clerk", { configurable: true, value: { loaded: false } });
    try {
      expect(await readClientSession(40, 5)).toBeNull();
    } finally {
      Object.defineProperty(window, "Clerk", { configurable: true, value: clerk });
    }
  });
});
