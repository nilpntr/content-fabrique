import { describe, expect, test } from "vitest";
import type { Entitlements, Me } from "@/lib/api";
import { decidePlanGate, isPaywallExempt } from "@/lib/paywall";

function meWith(entitlements: Partial<Entitlements> | undefined): Me {
  return {
    user: { id: "usr_1", email: "ada@example.com" },
    workspace: { id: "wks_1", name: "Fit Studio", role: "owner" },
    entitlements: entitlements as Entitlements,
  };
}
const active = { plan: "creator", status: "active", active: true, current_period_end: null } as const;
const none = { plan: "none", status: "none", active: false, current_period_end: null } as const;

describe("decidePlanGate", () => {
  test("an active plan opens the app, anything else is the paywall", () => {
    expect(decidePlanGate("/", { isPending: false, isError: false, data: meWith(active) })).toBe("app");
    expect(decidePlanGate("/", { isPending: false, isError: false, data: meWith(none) })).toBe("paywall");
    // An expired grant: the plan is still named, but it is not active.
    expect(decidePlanGate("/reports", { isPending: false, isError: false, data: meWith({ ...active, active: false }) })).toBe("paywall");
  });

  test("it fails closed: a missing or odd entitlements field is the paywall", () => {
    expect(decidePlanGate("/", { isPending: false, isError: false, data: meWith(undefined) })).toBe("paywall");
    expect(decidePlanGate("/", { isPending: false, isError: false, data: meWith({ active: "yes" as unknown as boolean }) })).toBe("paywall");
  });

  test("while the plan is unknown nothing is shown, and a failed request is an error, not a paywall", () => {
    expect(decidePlanGate("/", { isPending: true, isError: false })).toBe("loading");
    expect(decidePlanGate("/", { isPending: false, isError: true })).toBe("error");
    expect(decidePlanGate("/", { isPending: false, isError: false })).toBe("error");
  });

  test("Settings stays open whatever the plan or the request did", () => {
    for (const me of [
      { isPending: true, isError: false },
      { isPending: false, isError: true },
      { isPending: false, isError: false, data: meWith(none) },
    ]) {
      expect(decidePlanGate("/settings", me)).toBe("app");
      expect(decidePlanGate("/settings/", me)).toBe("app");
    }
  });
});

describe("isPaywallExempt", () => {
  test("only Settings, not look-alikes", () => {
    expect(isPaywallExempt("/settings")).toBe(true);
    expect(isPaywallExempt("/Settings/")).toBe(true);
    expect(isPaywallExempt("/settings/profile")).toBe(true);
    for (const path of ["/", "/reports", "/projects/new", "/settingsx", "/x/settings", "/not-a-page"]) {
      expect(isPaywallExempt(path)).toBe(false);
    }
  });
});
