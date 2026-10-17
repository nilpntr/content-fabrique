import * as React from "react";
import { usePrefersReducedMotion } from "@/hooks/use-prefers-reduced-motion";
import { claimPreview, releasePreview, useHoldsPreview } from "./preview-coordinator";
import { scrubFraction } from "./scrub";

/** How long the pointer rests on a card before its preview starts. */
export const HOVER_DELAY_MS = 300;
/** How long a finger is held on a card before its preview starts. */
export const LONG_PRESS_MS = 400;
/** A finger that moves further than this before the long press fires is scrolling, not pressing. */
export const LONG_PRESS_SLOP_PX = 10;
/** The pointer has to travel this far before the sprite scrub takes over from the video, so a resting hand does not. */
export const SCRUB_START_PX = 6;
/** The scrub frame stays this long after the pointer stops, then the video (if any) shows again. */
export const SCRUB_IDLE_MS = 600;

export interface ClipPreviewOptions {
  id: string;
  /** Only ready clips preview or scrub. */
  enabled: boolean;
  /** There is a hover preview video to play. */
  hasVideo: boolean;
  /** There is a sprite to scrub. */
  hasSprite: boolean;
}

type Timer = ReturnType<typeof setTimeout>;

/**
 * The state machine behind play-on-hover. The pointer (or keyboard focus) resting on the card for 300 ms mounts the
 * muted video; a long press does the same on touch. Moving across the card scrubs the sprite instead. Leaving stops it all.
 * Nothing here touches the network: the card mounts the `<video>` only while `showVideo` is true.
 */
export function useClipPreview({ id, enabled, hasVideo, hasSprite }: ClipPreviewOptions) {
  const reducedMotion = usePrefersReducedMotion();
  const holdsSlot = useHoldsPreview(id);
  const [hovering, setHovering] = React.useState(false);
  const [wantsVideo, setWantsVideo] = React.useState(false);
  const [scrub, setScrub] = React.useState<number | null>(null);

  const hoverTimer = React.useRef<Timer>(undefined);
  const idleTimer = React.useRef<Timer>(undefined);
  const pressTimer = React.useRef<Timer>(undefined);
  const press = React.useRef<{ x: number; y: number; fired: boolean } | null>(null);
  const suppressClick = React.useRef(false);
  const anchorX = React.useRef<number | null>(null);
  const canPlay = hasVideo && !reducedMotion;

  const clearTimers = React.useCallback(() => {
    clearTimeout(hoverTimer.current);
    clearTimeout(idleTimer.current);
    clearTimeout(pressTimer.current);
  }, []);

  const end = React.useCallback(() => {
    clearTimers();
    press.current = null;
    anchorX.current = null;
    setHovering(false);
    setWantsVideo(false);
    setScrub(null);
    releasePreview(id);
  }, [clearTimers, id]);

  // A clip that stops being previewable (it was reprocessed, say) while the pointer is on it stops at once.
  React.useEffect(() => {
    if (!enabled) end();
  }, [enabled, end]);
  React.useEffect(() => end, [end]);

  function startVideo() {
    if (!canPlay) return;
    claimPreview(id);
    setWantsVideo(true);
  }

  function begin(clientX?: number) {
    if (!enabled) return;
    setHovering(true);
    anchorX.current = clientX ?? null;
    clearTimeout(hoverTimer.current);
    hoverTimer.current = setTimeout(startVideo, HOVER_DELAY_MS);
  }

  function track(clientX: number, rect: { left: number; width: number }) {
    if (!enabled || !hasSprite) return;
    if (scrub === null && anchorX.current !== null && Math.abs(clientX - anchorX.current) < SCRUB_START_PX) return;
    setScrub(scrubFraction(clientX, rect));
    clearTimeout(idleTimer.current);
    idleTimer.current = setTimeout(() => {
      anchorX.current = clientX;
      setScrub(null);
    }, SCRUB_IDLE_MS);
  }

  const handlers = {
    onPointerEnter(event: React.PointerEvent<HTMLElement>) {
      if (event.pointerType === "touch") return;
      begin(event.clientX);
    },
    onPointerLeave(event: React.PointerEvent<HTMLElement>) {
      if (event.pointerType === "touch") return;
      end();
    },
    onPointerDown(event: React.PointerEvent<HTMLElement>) {
      suppressClick.current = false;
      if (event.pointerType !== "touch" || !enabled) return;
      press.current = { x: event.clientX, y: event.clientY, fired: false };
      clearTimeout(pressTimer.current);
      pressTimer.current = setTimeout(() => {
        if (!press.current) return;
        press.current.fired = true;
        suppressClick.current = true;
        setHovering(true);
        startVideo();
      }, LONG_PRESS_MS);
    },
    onPointerMove(event: React.PointerEvent<HTMLElement>) {
      const rect = event.currentTarget.getBoundingClientRect();
      if (event.pointerType !== "touch") {
        track(event.clientX, rect);
        return;
      }
      const current = press.current;
      if (!current) return;
      if (current.fired) {
        setScrub(scrubFraction(event.clientX, rect));
      } else if (Math.hypot(event.clientX - current.x, event.clientY - current.y) > LONG_PRESS_SLOP_PX) {
        clearTimeout(pressTimer.current);
        press.current = null;
      }
    },
    onPointerUp(event: React.PointerEvent<HTMLElement>) {
      if (event.pointerType === "touch") releaseTouch();
    },
    onPointerCancel(event: React.PointerEvent<HTMLElement>) {
      if (event.pointerType === "touch") releaseTouch();
    },
    // The long press must not open the browser's context menu (Android) or select the label (iOS).
    onContextMenu(event: React.MouseEvent<HTMLElement>) {
      if (press.current) event.preventDefault();
    },
    onFocus(event: React.FocusEvent<HTMLElement>) {
      if (focusVisible(event.target)) begin();
    },
    onBlur(event: React.FocusEvent<HTMLElement>) {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) end();
    },
  };

  function releaseTouch() {
    clearTimeout(pressTimer.current);
    if (press.current?.fired) end();
    press.current = null;
  }

  return {
    handlers,
    /** The pointer or focus is on the card: time to have the sprite loaded. */
    hovering,
    /** Mount the `<video>`: the delay passed and this card holds the one preview slot. */
    showVideo: wantsVideo && holdsSlot,
    /** 0 to 1 while the sprite scrub is showing, otherwise null. */
    scrub,
    /** True once for the click that follows a long press, which must not open the clip. */
    consumeClick() {
      const swallow = suppressClick.current;
      suppressClick.current = false;
      return swallow;
    },
  };
}

/** Keyboard focus only: a mouse click that leaves focus on the card must not start a preview. */
function focusVisible(target: EventTarget): boolean {
  if (!(target instanceof Element)) return false;
  try {
    return target.matches(":focus-visible");
  } catch {
    return true;
  }
}
