import { useId } from "react";
import { cn } from "@/lib/utils";

export const APP_NAME = "Dashboard";

export function LogoMark({ className }: { className?: string }) {
  const id = useId();
  return (
    <svg viewBox="0 0 32 32" fill="none" aria-hidden className={cn("size-8 shrink-0", className)}>
      <defs>
        <linearGradient id={`${id}-fill`} x1="0" y1="0" x2="0" y2="32" gradientUnits="userSpaceOnUse">
          <stop offset="0" style={{ stopColor: "var(--logo-top)" }} />
          <stop offset="1" style={{ stopColor: "var(--logo-bottom)" }} />
        </linearGradient>
        <linearGradient id={`${id}-edge`} x1="0" y1="0" x2="0" y2="32" gradientUnits="userSpaceOnUse">
          <stop offset="0" style={{ stopColor: "var(--logo-edge)" }} />
          <stop offset="0.45" style={{ stopColor: "var(--logo-edge)", stopOpacity: 0 }} />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="9.5" fill={`url(#${id}-fill)`} />
      <rect x="0.5" y="0.5" width="31" height="31" rx="9" stroke={`url(#${id}-edge)`} />
      <g className="origin-center transition-[rotate] duration-(--dur-spring) ease-spring [transform-box:fill-box] group-hover/logo:rotate-90">
        <rect x="8.5" y="8.5" width="6.5" height="6.5" rx="2" style={{ fill: "var(--logo-lit)" }} />
        <rect x="17" y="8.5" width="6.5" height="6.5" rx="2" style={{ fill: "var(--logo-tile)" }} opacity="0.9" />
        <rect x="8.5" y="17" width="6.5" height="6.5" rx="2" style={{ fill: "var(--logo-tile)" }} opacity="0.5" />
        <rect x="17" y="17" width="6.5" height="6.5" rx="2" style={{ fill: "var(--logo-tile)" }} opacity="0.5" />
      </g>
    </svg>
  );
}

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-2.5 group-data-[collapsible=icon]:shrink-0", className)}>
      <LogoMark />
      <span className="truncate text-[15px] font-semibold tracking-[-0.02em] transition-opacity delay-90 duration-(--dur-base) group-data-[collapsible=icon]:pointer-events-none group-data-[collapsible=icon]:shrink-0 group-data-[collapsible=icon]:overflow-visible group-data-[collapsible=icon]:opacity-0 group-data-[collapsible=icon]:delay-0 group-data-[collapsible=icon]:duration-(--dur-fast)">
        {APP_NAME}
      </span>
    </span>
  );
}
