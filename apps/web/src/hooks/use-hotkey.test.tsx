import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { type HotkeyOptions, useHotkey } from "./use-hotkey";

afterEach(cleanup);

function Probe({ hotkey, options, onKey }: { hotkey: string; options?: HotkeyOptions; onKey: () => void }) {
  useHotkey(hotkey, onKey, options);
  return (
    <>
      <input aria-label="field" />
      <textarea aria-label="notes" />
      <div data-testid="editor" contentEditable suppressContentEditableWarning />
      <button type="button">button</button>
    </>
  );
}

function setup(options?: HotkeyOptions, hotkey = "b") {
  const onKey = vi.fn();
  const view = render(<Probe hotkey={hotkey} options={options} onKey={onKey} />);
  // jsdom does not implement isContentEditable.
  Object.defineProperty(view.getByTestId("editor"), "isContentEditable", { value: true });
  return { onKey, view };
}

function press(target: EventTarget, init: KeyboardEventInit) {
  const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
}

test("a mod key fires with Ctrl or Cmd, and nothing else does", () => {
  const { onKey } = setup({ mod: true });
  fireEvent.keyDown(window, { key: "b", ctrlKey: true });
  fireEvent.keyDown(window, { key: "B", metaKey: true });
  expect(onKey).toHaveBeenCalledTimes(2);
  fireEvent.keyDown(window, { key: "b" });
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  expect(onKey).toHaveBeenCalledTimes(2);
});

test("Alt blocks the shortcut", () => {
  const { onKey } = setup({ mod: true });
  fireEvent.keyDown(window, { key: "b", ctrlKey: true, altKey: true });
  expect(onKey).not.toHaveBeenCalled();
});

test("with allowWhileTyping off, Ctrl+B in a text field is left to the field", () => {
  const { onKey, view } = setup({ mod: true, allowWhileTyping: false });
  for (const field of [view.getByLabelText("field"), view.getByLabelText("notes"), view.getByTestId("editor")]) {
    const event = press(field, { key: "b", ctrlKey: true });
    expect(event.defaultPrevented).toBe(false);
  }
  expect(onKey).not.toHaveBeenCalled();

  // Anywhere else it still works, and the browser's own handling is cancelled.
  const event = press(view.getByText("button"), { key: "b", ctrlKey: true });
  expect(onKey).toHaveBeenCalledTimes(1);
  expect(event.defaultPrevented).toBe(true);
});

test("a mod shortcut works inside a text field by default, as the command palette needs", () => {
  const { onKey, view } = setup({ mod: true }, "k");
  press(view.getByLabelText("field"), { key: "k", metaKey: true });
  expect(onKey).toHaveBeenCalledTimes(1);
});

test("a bare key is ignored while typing", () => {
  const { onKey, view } = setup(undefined, "/");
  press(view.getByLabelText("field"), { key: "/" });
  expect(onKey).not.toHaveBeenCalled();
  press(view.getByText("button"), { key: "/" });
  expect(onKey).toHaveBeenCalledTimes(1);
});
