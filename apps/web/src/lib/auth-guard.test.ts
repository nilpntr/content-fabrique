import { describe, expect, test } from "vitest";
import { decideAccess, isAuthPath, isBarePath, type Session, signInHref } from "./auth-guard";

const signedOut: Session = { isAuthenticated: false, userId: null, orgId: null };
const noWorkspace: Session = { isAuthenticated: true, userId: "user_1", orgId: null };
const member: Session = { isAuthenticated: true, userId: "user_1", orgId: "org_1" };

describe("a signed-out visitor", () => {
  test.each(["/", "/reports", "/settings", "/reports/", "/Settings", "/anything/else", "/workspace", "/sign-inn", "/sign-in-not"])(
    "is sent to sign in from %s",
    (path) => {
      const decision = decideAccess(path, signedOut);
      expect(decision.kind).toBe("redirect");
      expect(decision.kind === "redirect" && decision.to.startsWith("/sign-in")).toBe(true);
    },
  );

  test("may open the sign-in and sign-up pages and their steps", () => {
    for (const path of ["/sign-in", "/sign-in/", "/sign-in/factor-one", "/sign-in/tasks/choose-organization", "/sign-up", "/sign-up/verify-email-address", "/SIGN-IN"]) {
      expect(decideAccess(path, signedOut)).toEqual({ kind: "allow" });
    }
  });

  test("comes back to the page they asked for after signing in", () => {
    expect(decideAccess("/", signedOut)).toEqual({ kind: "redirect", to: "/sign-in" });
    expect(decideAccess("/settings", signedOut)).toEqual({ kind: "redirect", to: "/sign-in?redirect_url=%2Fsettings" });
    expect(decideAccess("/reports", signedOut, "?range=7d")).toEqual({
      kind: "redirect",
      to: "/sign-in?redirect_url=%2Freports%3Frange%3D7d",
    });
  });
});

describe("a signed-in user without an active workspace", () => {
  test("only gets the workspace picker", () => {
    expect(decideAccess("/workspace", noWorkspace)).toEqual({ kind: "allow" });
    for (const path of ["/", "/reports", "/settings", "/anything"]) {
      expect(decideAccess(path, noWorkspace)).toEqual({ kind: "redirect", to: "/workspace" });
    }
  });

  test("can finish Clerk's sign-in steps", () => {
    expect(decideAccess("/sign-in/tasks/choose-organization", noWorkspace)).toEqual({ kind: "allow" });
  });
});

describe("a member with an active workspace", () => {
  test("gets the app", () => {
    for (const path of ["/", "/reports", "/settings", "/anything"]) {
      expect(decideAccess(path, member)).toEqual({ kind: "allow" });
    }
  });

  test("does not need the workspace picker", () => {
    expect(decideAccess("/workspace", member)).toEqual({ kind: "redirect", to: "/" });
  });
});

describe("paths", () => {
  test("sign-in only matches whole path segments", () => {
    expect(isAuthPath("/sign-in")).toBe(true);
    expect(isAuthPath("/sign-in/x")).toBe(true);
    expect(isAuthPath("/sign-inx")).toBe(false);
    expect(isAuthPath("/x/sign-in")).toBe(false);
    expect(isAuthPath("/")).toBe(false);
  });

  test("the app shell is for the app's pages only", () => {
    expect(isBarePath("/sign-in")).toBe(true);
    expect(isBarePath("/workspace")).toBe(true);
    expect(isBarePath("/settings")).toBe(false);
  });

  test("the return address is always a path on this site", () => {
    expect(signInHref("//evil.example")).toBe("/sign-in");
    expect(signInHref("/sign-in")).toBe("/sign-in");
    expect(signInHref("/a", "?next=https://evil.example")).toBe("/sign-in?redirect_url=%2Fa%3Fnext%3Dhttps%3A%2F%2Fevil.example");
  });
});
