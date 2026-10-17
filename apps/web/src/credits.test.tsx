import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { CreditsChip, creditsSummary } from "@/components/credits";
import { startEditMessage } from "@/components/project/errors";
import { ApiError } from "@/lib/api/client";
import { announceCreditsExhausted, creditsOf, dismissCreditsExhausted, formatDay, isCreditsExhausted, useCreditsExhausted } from "@/lib/credits";
import { createQueryClient } from "@/lib/query-client";
import { getRouter } from "./router";
import { installApi } from "./test-api";
import { ME } from "./test-clerk";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  act(() => dismissCreditsExhausted());
  cleanup();
});

const CREDITS = { available: 42, allowance: 60, period_end: "2099-11-14T00:00:00Z", low: false, top_ups: [{ credits: 20, expires_at: "2099-12-01T00:00:00Z" }] };

function renderAt(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

// ---- Reading the balance ----------------------------------------------------------------------------------------------------

test("the balance is read from /v1/me only when it is there and well formed", () => {
  expect(creditsOf(ME)).toBeUndefined();
  expect(creditsOf(undefined)).toBeUndefined();
  expect(creditsOf({ credits: "lots" })).toBeUndefined();
  expect(creditsOf({ credits: { available: -1 } })).toBeUndefined();
  expect(creditsOf({ ...ME, credits: CREDITS })).toEqual(CREDITS);
  // Left out by the API when it cannot read the balance: the screens stay quiet.
  expect(creditsOf({ ...ME, credits: { available: 3 } })).toEqual({ available: 3, allowance: 0, period_end: null, low: false, top_ups: [] });
  expect(creditsOf({ credits: { ...CREDITS, top_ups: [null, { credits: 5 }, { credits: 5, expires_at: "2099-01-01T00:00:00Z" }] } })?.top_ups).toEqual([{ credits: 5, expires_at: "2099-01-01T00:00:00Z" }]);
});

test("days are short, with the year only when it is another one", () => {
  const now = new Date("2026-10-17T10:00:00Z");
  expect(formatDay("2026-11-14T00:00:00Z", now)).toBe("14 Nov");
  expect(formatDay("2027-01-02T12:00:00Z", now)).toBe("2 Jan 2027");
  expect(formatDay("not a date", now)).toBe("");
});

// ---- The chip -----------------------------------------------------------------------------------------------------------------

test("the chip is not there without a balance", () => {
  const { container } = render(<CreditsChip credits={undefined} />);
  expect(container.firstChild).toBeNull();
});

test("the shell has no credits chip until /v1/me reports a balance, and then shows it", async () => {
  installApi({ "GET /v1/me": ME });
  renderAt("/reports");
  await screen.findByRole("heading", { name: "Reports" });
  await screen.findByTestId("sidebar-workspace");
  expect(screen.queryByTestId("credits-chip")).toBeNull();

  act(() => root?.unmount());
  installApi({ "GET /v1/me": { ...ME, credits: CREDITS } });
  renderAt("/reports");
  const chip = await screen.findByTestId("credits-chip");
  expect(chip.getAttribute("data-state")).toBe("ok");
  expect(chip.textContent).toContain("42");
  expect(chip.getAttribute("aria-label")).toBe(creditsSummary(CREDITS as never));
  expect(chip.getAttribute("href")).toBe("/settings");
});

test("a nearly used allowance and an empty one change the chip", async () => {
  installApi({ "GET /v1/me": { ...ME, credits: { ...CREDITS, available: 6, low: true } } });
  renderAt("/reports");
  expect((await screen.findByTestId("credits-chip")).getAttribute("data-state")).toBe("low");

  act(() => root?.unmount());
  installApi({ "GET /v1/me": { ...ME, credits: { ...CREDITS, available: 0, low: true } } });
  renderAt("/reports");
  await waitFor(() => expect(screen.getByTestId("credits-chip").getAttribute("data-state")).toBe("empty"));
});

test("the summary names what is left, the allowance and the renewal", () => {
  expect(creditsSummary({ available: 1, allowance: 0, period_end: null, low: false, top_ups: [] })).toBe("1 credit left");
  expect(creditsSummary({ ...CREDITS, period_end: "2099-11-14T00:00:00Z" })).toBe("42 credits left, 60 in this period's allowance, renews 14 Nov 2099");
});

test("Settings spells the balance out, with the top-ups, and has no card without one", async () => {
  installApi({ "GET /v1/me": { ...ME, credits: { ...CREDITS, available: 12, low: true } } });
  renderAt("/settings");
  const card = await screen.findByTestId("credits-card");
  expect(card.textContent).toContain("12");
  expect(card.textContent).toContain("Running low");
  expect(card.textContent).toContain("48 of 60 used this period");
  expect(card.textContent).toContain("20 credits");
  expect(card.textContent).toContain("valid until 1 Dec 2099");

  act(() => root?.unmount());
  installApi({ "GET /v1/me": ME });
  renderAt("/settings");
  await screen.findByText("Fit Studio", { selector: "dd" });
  expect(screen.queryByTestId("credits-card")).toBeNull();
});

// ---- 402 credits_exhausted, by its code -----------------------------------------------------------------------------------------

test("any call refused with 402 credits_exhausted announces it, and a 402 plan_required or another error does not", async () => {
  function Probe() {
    return <span data-testid="probe">{String(useCreditsExhausted())}</span>;
  }
  render(<Probe />);
  const client = createQueryClient();
  const fail = (error: ApiError) => client.getMutationCache().build(client, { mutationFn: () => Promise.reject(error) }).execute(undefined).catch(() => {});

  await act(async () => fail(new ApiError(402, "plan_required", "no plan")));
  await act(async () => fail(new ApiError(409, "run_in_progress", "busy")));
  expect(screen.getByTestId("probe").textContent).toBe("false");

  await act(async () => fail(new ApiError(402, "credits_exhausted", "no credits")));
  expect(screen.getByTestId("probe").textContent).toBe("true");
  // A query is caught the same way.
  act(() => dismissCreditsExhausted());
  expect(screen.getByTestId("probe").textContent).toBe("false");
  await act(async () => {
    await client.fetchQuery({ queryKey: ["x"], queryFn: () => Promise.reject(new ApiError(402, "credits_exhausted", "no credits")), retry: false }).catch(() => {});
  });
  expect(screen.getByTestId("probe").textContent).toBe("true");
  expect(isCreditsExhausted(new ApiError(402, "credits_exhausted", ""))).toBe(true);
  expect(isCreditsExhausted(new ApiError(403, "credits_exhausted", ""))).toBe(false);
  expect(isCreditsExhausted(new Error("credits_exhausted"))).toBe(false);
});

test("the out-of-credits dialog says what happened, what is left and when it renews, and closes", async () => {
  installApi({ "GET /v1/me": { ...ME, credits: { ...CREDITS, available: 1 } } });
  renderAt("/reports");
  await screen.findByTestId("credits-chip");
  act(() => announceCreditsExhausted());
  const dialog = await screen.findByRole("dialog");
  expect(dialog.textContent).toContain("You're out of credits");
  expect(dialog.textContent).toContain("Nothing was charged");
  expect(dialog.textContent).toContain("renews on 14 Nov 2099");
  expect(dialog.textContent).toContain("1 credit left");
  fireEvent.click(screen.getByRole("button", { name: "Close", hidden: false }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("starting an edit with no credits says so, and a missing plan still says that", () => {
  expect(startEditMessage(new ApiError(402, "credits_exhausted", ""))).toBe("Your workspace doesn't have enough credits for an edit.");
  expect(startEditMessage(new ApiError(402, "plan_required", ""))).toBe("Your workspace needs an active plan to use the editor.");
});

test("the dialog works without a balance too", async () => {
  installApi({ "GET /v1/me": ME });
  renderAt("/reports");
  await screen.findByTestId("sidebar-workspace");
  act(() => announceCreditsExhausted());
  const dialog = await screen.findByRole("dialog");
  expect(dialog.textContent).toContain("You're out of credits");
  expect(dialog.textContent).not.toContain("renews");
});
