import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { installApi, renderApp } from "@/test-api";
import { emptyStyle, learntStyle, styleReference } from "@/test-fixtures";
import { StylePage } from "./style";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ to, children, className }: { to: string; children: ReactNode; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));
const notify = vi.fn();
vi.mock("@/components/ui/toast", () => ({ notify: (...args: unknown[]) => notify(...args) }));
vi.mock("@/lib/upload/provider", () => ({
  useUploadCommands: () => ({ available: true, addFiles: vi.fn() }),
  useUploadSelector: () => new Map(),
}));

afterEach(() => {
  cleanup();
  notify.mockReset();
});

const me = (role = "owner") => ({
  user: { id: "usr_01test", email: "ada@example.com" },
  workspace: { id: "wks_01test", name: "Fit Studio", role },
  entitlements: { plan: "creator", status: "active", active: true, current_period_end: null },
});

test("the AI's sentence, the videos and the settings; the numbers are behind a closed Details", async () => {
  installApi({ "GET /v1/me": me(), "GET /v1/style": learntStyle() });
  renderApp(<StylePage />);
  expect(await screen.findByText(/Handheld gym shots that follow you/)).toBeTruthy();
  expect(screen.getByText("Written by AI from 5 of your videos.")).toBeTruthy();
  expect(screen.getAllByText("Learnt")).toHaveLength(5);
  // The creator's own transitions show as theirs; the others say where they came from.
  const transitions = screen.getByRole("radiogroup", { name: "Transitions" });
  expect((within(transitions).getByRole("radio", { name: "Lots" }) as HTMLInputElement).checked).toBe(true);
  expect(screen.getAllByRole("button", { name: "Use suggestion" })).toHaveLength(1);
  expect(screen.getAllByText("Suggested from your videos").length).toBeGreaterThan(0);
  // The measured numbers are there, closed.
  const details = screen.getByText("Long takes that let the set run: 0 to 5 cuts a video, the longest shot about 20 s").closest("details");
  expect(details?.open).toBe(false);
  // No jargon on the page.
  expect(document.body.textContent).not.toMatch(/LUFS|BPM|luma|p90|verdict|stylesummary/);
});

test("picking a setting saves the whole set of the creator's settings, and the vibe too", async () => {
  const api = installApi({ "GET /v1/me": me(), "GET /v1/style": learntStyle(), "PUT /v1/style": ({ body }) => ({ ...learntStyle(), settings: (body as { settings: object }).settings }) });
  renderApp(<StylePage />);
  const camera = await screen.findByRole("radiogroup", { name: "Camera moves" });
  fireEvent.click(within(camera).getByRole("radio", { name: "Off" }));
  await waitFor(() => expect(api.callsTo("PUT /v1/style")).toHaveLength(1));
  expect(api.callsTo("PUT /v1/style")[0]?.body).toEqual({ settings: { transitions: "lots", camera_motion: "off" } });

  fireEvent.click(screen.getByRole("radio", { name: /Hype/ }));
  await waitFor(() => expect(api.callsTo("PUT /v1/style")).toHaveLength(2));
  const second = api.callsTo("PUT /v1/style")[1]?.body as { settings: { vibe?: string } } | undefined;
  expect(second?.settings.vibe).toBe("hype");

  // Giving a setting back to the suggestion leaves it out.
  fireEvent.click(screen.getAllByRole("button", { name: "Use suggestion" })[0] as HTMLElement);
  await waitFor(() => expect(api.callsTo("PUT /v1/style")).toHaveLength(3));
  const third = api.callsTo("PUT /v1/style")[2]?.body as { settings: Record<string, string> } | undefined;
  expect(third?.settings).toBeDefined();
  expect(third?.settings.transitions).toBeUndefined();
});

test("a reference can be removed, and the style can be learnt again", async () => {
  const api = installApi({
    "GET /v1/me": me(),
    "GET /v1/style": learntStyle(),
    "DELETE /v1/style/references/{id}": () => null,
    "POST /v1/style/relearn": () => ({ ...learntStyle(), status: "learning", progress: { done: 0, total: 5 } }),
  });
  renderApp(<StylePage />);
  fireEvent.click(await screen.findByRole("button", { name: "Remove video-2.mp4" }));
  await waitFor(() => expect(api.callsTo("DELETE /v1/style/references/{id}")).toHaveLength(1));
  expect(api.callsTo("DELETE /v1/style/references/{id}")[0]?.path).toBe(`/v1/style/references/${styleReference(2).asset_id}`);
  fireEvent.click(screen.getByRole("button", { name: "Learn again" }));
  expect(await screen.findByText("Learning your style… 0 of 5 videos done")).toBeTruthy();
});

test("a new workspace is asked for its videos; a viewer cannot change anything", async () => {
  installApi({ "GET /v1/me": me("viewer"), "GET /v1/style": emptyStyle() });
  renderApp(<StylePage />);
  expect(await screen.findByText(/Add 3 to 10 of your own finished videos below/)).toBeTruthy();
  expect(await screen.findAllByText("Only owners and editors can change the style.")).not.toHaveLength(0);
  const transitions = screen.getByRole("radiogroup", { name: "Transitions" });
  expect((within(transitions).getByRole("radio", { name: "A few" }) as HTMLInputElement).disabled).toBe(true);
});

test("while it learns, the page says how far it is", async () => {
  installApi({
    "GET /v1/me": me(),
    "GET /v1/style": emptyStyle({ status: "learning", progress: { done: 1, total: 3 }, references: [styleReference(1), styleReference(2, { status: "analysing" }), styleReference(3, { status: "processing", progress: 40 })] }),
  });
  renderApp(<StylePage />);
  expect(await screen.findByText("Learning your style… 1 of 3 videos done")).toBeTruthy();
  expect(screen.getByText("Getting it ready… 40 %")).toBeTruthy();
  expect(screen.getByText("Learning from it…")).toBeTruthy();
});
