import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { CaptionWordEditor, captionFixMessage } from "./caption-fixer";
import { ApiError } from "@/lib/api/client";

afterEach(cleanup);

const words = [{ text: "Never" }, { text: "skip" }, { text: "creating," }, { text: "ever." }];

test("a click on a word opens it, and Save sends the clean fix for that word", () => {
  const onFix = vi.fn();
  render(<CaptionWordEditor words={words} fixes={new Map()} onFix={onFix} />);
  fireEvent.click(screen.getByRole("button", { name: "Fix the word creating," }));
  const box = screen.getByLabelText("How it should read") as HTMLInputElement;
  expect(box.value).toBe("creating,");
  fireEvent.change(box, { target: { value: "  creatine,  " } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(onFix).toHaveBeenCalledWith(2, "creatine,");
  expect(screen.queryByLabelText("How it should read")).toBeNull();
});

test("a hostile fix is explained and never sent", () => {
  const onFix = vi.fn();
  render(<CaptionWordEditor words={words} fixes={new Map()} onFix={onFix} />);
  fireEvent.click(screen.getByRole("button", { name: "Fix the word skip" }));
  fireEvent.change(screen.getByLabelText("How it should read"), { target: { value: "{\\an8}skip" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(screen.getByRole("alert").textContent).toMatch(/letters, numbers/);
  expect(onFix).not.toHaveBeenCalled();
});

test("a fixed word shows the fix and can go back to what was heard", () => {
  const onFix = vi.fn();
  render(<CaptionWordEditor words={words} fixes={new Map([[2, "creatine,"]])} onFix={onFix} />);
  expect(screen.getByRole("button", { name: "Fix the word creatine, (heard as creating,)" }).textContent).toBe("creatine,");
  fireEvent.click(screen.getByRole("button", { name: "Fix the word creatine, (heard as creating,)" }));
  fireEvent.click(screen.getByRole("button", { name: "Back to original" }));
  expect(onFix).toHaveBeenCalledWith(2, null);
});

test("the messages of a fix that was refused", () => {
  expect(captionFixMessage(new ApiError(409, "version_conflict", "x"))).toMatch(/changed/);
  expect(captionFixMessage(new ApiError(422, "invalid_ops", "x"))).toMatch(/could not be saved/);
  expect(captionFixMessage(new Error("boom"))).toMatch(/Try again/);
});
