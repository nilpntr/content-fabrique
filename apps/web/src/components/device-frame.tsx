import type * as React from "react";
import { cn } from "@/lib/utils";

export type DeviceKind = "phone" | "feed-post" | "laptop";
export type PostRatio = "1:1" | "4:5";

/** Heights by size. The laptop is wider than it is tall, so it gets less height to end up the same visual weight. */
const SIZES: Record<DeviceKind, Record<"sm" | "md", string>> = {
  phone: { sm: "h-14", md: "h-40" },
  "feed-post": { sm: "h-14", md: "h-40" },
  laptop: { sm: "h-11", md: "h-31" },
};

/**
 * Colours come from four custom properties so the drawing follows the theme tokens and flips with `active`.
 * Strokes are non-scaling, so the outline stays one crisp pixel-ish line at any size.
 */
const PALETTE = [
  "[--d-edge:color-mix(in_oklab,var(--foreground)_70%,transparent)]",
  "[--d-screen:color-mix(in_oklab,var(--foreground)_5%,var(--surface-raised))]",
  "[--d-hero:color-mix(in_oklab,var(--foreground)_11%,transparent)]",
  "[--d-ink:color-mix(in_oklab,var(--foreground)_26%,transparent)]",
  "data-[active=true]:[--d-edge:var(--brand)]",
  "data-[active=true]:[--d-screen:color-mix(in_oklab,var(--brand)_10%,var(--surface-raised))]",
  "data-[active=true]:[--d-hero:color-mix(in_oklab,var(--brand)_30%,transparent)]",
  "data-[active=true]:[--d-ink:color-mix(in_oklab,var(--brand)_55%,transparent)]",
].join(" ");

const EDGE = "fill-surface-raised stroke-(--d-edge)";
const SCREEN = "fill-(--d-screen)";
const HERO = "fill-(--d-hero)";
const INK = "fill-(--d-ink)";

/** A phone body whose screen is exactly 9:16 (54 by 96). Content goes in its children. */
function Phone({ children }: { children: React.ReactNode }) {
  return (
    <>
      <rect x="1" y="1" width="62" height="106" rx="10" className={EDGE} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
      <rect x="5" y="5" width="54" height="96" rx="6.5" className={SCREEN} />
      {children}
      <rect x="26" y="7.5" width="12" height="3" rx="1.5" className="fill-(--d-edge)" />
    </>
  );
}

/** A creator in the hero area, drawn as two shapes. */
function Figure({ x, y, scale = 1 }: { x: number; y: number; scale?: number }) {
  return (
    <g transform={`translate(${x} ${y}) scale(${scale})`} className={HERO}>
      <circle cx="0" cy="-7" r="6" />
      <path d="M-13 14c0-12 6-20 13-20s13 8 13 20z" />
    </g>
  );
}

function ShortVideo() {
  return (
    <Phone>
      <Figure x={32} y={46} scale={1.25} />
      <circle cx="52" cy="60" r="2.6" className={INK} />
      <circle cx="52" cy="69" r="2.6" className={INK} />
      <circle cx="52" cy="78" r="2.6" className={INK} />
      <circle cx="11" cy="84" r="2.6" className={INK} />
      <rect x="17" y="82.8" width="18" height="2.2" rx="1.1" className={INK} />
      <rect x="9" y="89" width="30" height="2.2" rx="1.1" className={INK} />
      <rect x="9" y="94" width="20" height="2.2" rx="1.1" className={INK} />
    </Phone>
  );
}

function FeedPost({ ratio }: { ratio: PostRatio }) {
  const height = ratio === "1:1" ? 54 : 67.5;
  const below = 22 + height;
  return (
    <Phone>
      <circle cx="11" cy="16" r="3" className={INK} />
      <rect x="17" y="14.9" width="18" height="2.2" rx="1.1" className={INK} />
      <rect x="5" y="22" width="54" height={height} className={HERO} />
      <Figure x={32} y={22 + height / 2 + 1} scale={ratio === "1:1" ? 1.1 : 1.4} />
      <circle cx="10" cy={below + 5} r="1.8" className={INK} />
      <circle cx="17" cy={below + 5} r="1.8" className={INK} />
      <circle cx="24" cy={below + 5} r="1.8" className={INK} />
      <rect x="9" y={below + 9.5} width="34" height="2.2" rx="1.1" className={INK} />
      {ratio === "1:1" ? <rect x="9" y={below + 14} width="22" height="2.2" rx="1.1" className={INK} /> : null}
    </Phone>
  );
}

/** A laptop whose screen is exactly 16:9 (96 by 54), showing a video player. */
function Laptop() {
  return (
    <>
      <rect x="8" y="1" width="104" height="62" rx="4" className={EDGE} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
      <rect x="12" y="5" width="96" height="54" rx="1.5" className={SCREEN} />
      <Figure x={34} y={29} scale={1} />
      <circle cx="78" cy="28" r="7.5" className={INK} />
      <path d="M75.8 24.6 82 28l-6.2 3.4z" className={SCREEN} />
      <rect x="17" y="46" width="38" height="2.2" rx="1.1" className={INK} />
      <rect x="17" y="52" width="86" height="1.6" rx="0.8" className={INK} />
      <rect x="17" y="52" width="32" height="1.6" rx="0.8" className="fill-(--d-edge)" />
      <circle cx="60" cy="3" r="0.8" className="fill-(--d-edge)" />
      <path d="M2 63h116v2a4 4 0 0 1-4 4H6a4 4 0 0 1-4-4z" className={EDGE} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
      <rect x="50" y="63" width="20" height="2.4" rx="1.2" className="fill-(--d-edge)" />
    </>
  );
}

/**
 * A device drawn in the shape of an output format: a phone for 9:16, a phone showing a feed post for 1:1 and 4:5
 * (the post is drawn 54 wide and 54 or 67.5 tall), a laptop for 16:9. Pure SVG, so it is crisp at any size and
 * follows the theme tokens; it is decorative, so give the thing that contains it the accessible name.
 * `active` tints the drawing with the brand colour. The caller owns hover and selection motion.
 */
export function DeviceFrame({
  kind,
  postRatio = "1:1",
  active = false,
  size = "md",
  className,
}: {
  kind: DeviceKind;
  /** Only used when `kind` is "feed-post". */
  postRatio?: PostRatio;
  active?: boolean;
  size?: "sm" | "md";
  className?: string;
}) {
  const box = kind === "laptop" ? "0 0 120 70" : "0 0 64 108";
  return (
    <svg
      viewBox={box}
      aria-hidden="true"
      focusable="false"
      data-slot="device-frame"
      data-device={kind}
      data-active={active}
      className={cn(
        "w-auto max-w-full shrink-0 overflow-visible [&_*]:transition-colors [&_*]:duration-(--dur-base)",
        PALETTE,
        SIZES[kind][size],
        className,
      )}
    >
      {kind === "phone" ? <ShortVideo /> : kind === "feed-post" ? <FeedPost ratio={postRatio} /> : <Laptop />}
    </svg>
  );
}
