import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { NewProjectPage } from "./new-project";

afterEach(cleanup);

test("creating stays on the page and shows what would be sent", async () => {
  render(<NewProjectPage />);
  expect(screen.queryByText(/would be sent/)).toBeNull();

  fireEvent.click(screen.getByRole("radio", { name: /^1:1/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Back double biceps/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Barbell row/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  fireEvent.change(screen.getByRole("textbox", { name: /Instructions/ }), { target: { value: "Open on the pose" } });
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));

  const confirmation = await screen.findByRole("region", { name: /arrives with the API \(P0-08\)/ });
  expect(confirmation.textContent).toContain("1:1, 2 clips (0:35), with instructions");
  expect(confirmation.textContent).toContain('"brief": "Open on the pose"');
  // Still the flow, on the review step.
  await waitFor(() => expect(screen.getByRole("button", { name: "Create project" })).toBeTruthy());
});
