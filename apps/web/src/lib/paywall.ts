/**
 * Who sees the paywall. Pure logic, so it is tested without a router or Clerk.
 *
 * The rule mirrors the API's: a signed-in user whose workspace has no active plan sees the Paywall instead of every app
 * page, except the pages listed here. The API enforces it for real (402 `plan_required` on every paid endpoint), so
 * this decides what the browser shows, not what is allowed.
 */
import type { Me } from "@/lib/api";

/** The API's error code for a paid endpoint called without a plan. */
export const PLAN_REQUIRED_CODE = "plan_required";

/** Pages that stay open without a plan: account and workspace settings. Sign-in and the workspace picker are not app pages. */
export const PAYWALL_EXEMPT_PATHS: readonly string[] = ["/settings"];

function trimSlash(pathname: string): string {
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
}

export function isPaywallExempt(pathname: string): boolean {
  const path = trimSlash(pathname).toLowerCase();
  return PAYWALL_EXEMPT_PATHS.some((exempt) => path === exempt || path.startsWith(`${exempt}/`));
}

/**
 * Whether the workspace had a plan that has lapsed (expired, canceled, past due, or revoked by hand), as opposed to never
 * having had one. It only changes the words of the paywall.
 */
export function isLapsed(entitlements: Me["entitlements"] | undefined): boolean {
  if (!entitlements) return false;
  return entitlements.status === "canceled" || entitlements.status === "past_due" || entitlements.plan !== "none";
}

/** What to render for a page. `loading` is "ask again in a moment": the plan is not known yet. */
export type PlanGate = "app" | "paywall" | "loading" | "error";

export function decidePlanGate(pathname: string, me: { isPending: boolean; isError: boolean; data?: Me }): PlanGate {
  if (isPaywallExempt(pathname)) return "app";
  if (me.isPending) return "loading";
  // An unreachable API says so (the paywall would wrongly tell the user to buy something), and the app stays hidden.
  if (me.isError || !me.data) return "error";
  // The API answered, but could not read the plan: that is not "no plan", so it is not the paywall either.
  if (me.data.entitlements?.status === "unknown") return "error";
  // Fail closed in the browser too: anything but an explicit `active: true` is the paywall.
  return me.data.entitlements?.active === true ? "app" : "paywall";
}
