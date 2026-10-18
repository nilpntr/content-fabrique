import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { NewProjectFlow } from "@/components/new-project/new-project-flow";
import { SAMPLE_ASSETS } from "@/components/new-project/sample-data";
import { ExtraCarefulOption, extraCarefulCosts, extraCarefulText } from "./extra-careful";

afterEach(cleanup);

const COSTS = { standard: { first_edit: 6, revision: 2 }, max: { first_edit: 16, revision: 7 } };
const TRIAL_COSTS = { standard: { first_edit: 6, revision: 2 }, max: null };

test("the prices come from the API's edit costs, by kind of edit, and a plan without extra careful has none", () => {
  expect(extraCarefulCosts(COSTS, "first_edit")).toEqual({ extra: 16, standard: 6 });
  expect(extraCarefulCosts(COSTS, "revision")).toEqual({ extra: 7, standard: 2 });
  // The same code with another price list says another number: nothing is carried in the app.
  expect(extraCarefulCosts({ standard: { first_edit: 5, revision: 1 }, max: { first_edit: 12, revision: 4 } }, "first_edit")).toEqual({ extra: 12, standard: 5 });
  expect(extraCarefulCosts(TRIAL_COSTS, "first_edit")).toBeUndefined();
  expect(extraCarefulCosts(undefined, "revision")).toBeUndefined();
});

test("the option says what it does and what it costs, is off by default and reports its change", () => {
  const onChange = vi.fn();
  render(<ExtraCarefulOption price={{ extra: 16, standard: 6 }} checked={false} onChange={onChange} />);
  const box = screen.getByRole("checkbox", { name: "Extra careful" }) as HTMLInputElement;
  expect(box.checked).toBe(false);
  expect(screen.getByText("Thinks longer about every cut. Uses 16 credits instead of 6.")).toBeTruthy();
  expect(box.getAttribute("aria-describedby")).toBeTruthy();
  fireEvent.click(box);
  expect(onChange).toHaveBeenCalledWith(true);
  expect(extraCarefulText({ extra: 1, standard: 1 })).toBe("Thinks longer about every cut. Uses 1 credit instead of 1.");
});

test("without a price (the trial) nothing is shown", () => {
  const { container } = render(<ExtraCarefulOption price={undefined} checked={false} onChange={() => {}} />);
  expect(container.textContent).toBe("");
});

const next = () => fireEvent.click(screen.getByRole("button", { name: /^Next/ }));

function toBriefStep() {
  next(); // format
  fireEvent.click(screen.getByRole("checkbox", { name: /Back double biceps/ }));
  next(); // footage
}

test("New video: the option is off by default, and only a checked one makes the first edit extra careful", () => {
  const onCreate = vi.fn();
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} extraCareful={{ extra: 16, standard: 6 }} />);
  toBriefStep();
  const box = screen.getByRole("checkbox", { name: "Extra careful" }) as HTMLInputElement;
  expect(box.checked).toBe(false);
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  expect(onCreate).toHaveBeenLastCalledWith(expect.not.objectContaining({ quality: expect.anything() }));

  cleanup();
  onCreate.mockClear();
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={onCreate} extraCareful={{ extra: 16, standard: 6 }} />);
  toBriefStep();
  fireEvent.click(screen.getByRole("checkbox", { name: "Extra careful" }));
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  expect(screen.getByText(/Extra careful: the first edit uses 16 credits instead of 6/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({ quality: "max" }));
});

test("New video: a plan without extra careful (the trial) is not offered it", () => {
  render(<NewProjectFlow assets={SAMPLE_ASSETS} onCreate={vi.fn()} />);
  toBriefStep();
  expect(screen.queryByRole("checkbox", { name: "Extra careful" })).toBeNull();
});
