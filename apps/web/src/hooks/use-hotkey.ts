import { useEffect, useRef, useSyncExternalStore } from "react";

// Input types that take text. A checkbox, radio, range or button has focus without being typed into.
const TEXT_INPUT_TYPES = new Set(["text", "search", "email", "url", "tel", "password", "number"]);

function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable || target.tagName === "TEXTAREA") return true;
  return target instanceof HTMLInputElement && TEXT_INPUT_TYPES.has(target.type);
}

export interface HotkeyOptions {
  /** Needs Cmd (Apple) or Ctrl. Without it, the key must be pressed bare. */
  mod?: boolean;
  /** Needs Shift. Modifiers are exact: without it, Shift must not be held (Ctrl+Shift+B is not Ctrl+B). */
  shift?: boolean;
  /** Fire while a text field has focus. Defaults to `mod`: bare keys are typing, shortcuts such as Cmd+K are not. */
  allowWhileTyping?: boolean;
}

export function useHotkey(
  key: string,
  handler: (event: KeyboardEvent) => void,
  { mod = false, shift = false, allowWhileTyping = mod }: HotkeyOptions = {},
) {
  const latest = useRef(handler);
  latest.current = handler;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== key.toLowerCase()) return;
      const withMod = event.metaKey || event.ctrlKey;
      if (mod !== withMod || shift !== event.shiftKey || event.altKey) return;
      if (!allowWhileTyping && typing(event.target)) return;
      event.preventDefault();
      latest.current(event);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [key, mod, shift, allowWhileTyping]);
}

const noSubscription = () => () => {};

function platformModKey(): "⌘" | "Ctrl" {
  return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? "⌘" : "Ctrl";
}

/** "⌘" on Apple platforms, "Ctrl" elsewhere. The server renders "Ctrl", so hydration matches and Apple browsers switch after. */
export function useModKey(): "⌘" | "Ctrl" {
  return useSyncExternalStore(noSubscription, platformModKey, () => "Ctrl");
}
