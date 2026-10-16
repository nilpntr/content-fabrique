import * as React from "react";
import { Glider, glideToHighlighted } from "@/lib/glide";
import { cn } from "@/lib/utils";

/** Must be the first child of a `relative isolate` list. */
export function HighlightGlider({ className }: { className?: string }) {
  const ref = React.useRef<HTMLSpanElement>(null);
  React.useLayoutEffect(() => {
    const el = ref.current;
    const list = el?.parentElement;
    if (!el || !list) return;
    return glideToHighlighted(list, new Glider(el, list));
  }, []);
  return <span ref={ref} aria-hidden data-slot="highlight-glider" className={cn("glider -z-1", className)} />;
}
