/**
 * The few shapes every screen of the guided flow is made of: a narrow column, a big button, a big answer, and the card that says
 * what went wrong with the one thing to do about it. Big enough for a thumb (56 px), one accent colour, and a state is always an
 * icon with words, never a colour alone.
 */
import { ArrowLeft, ChevronRight, CircleAlert } from "lucide-react";
import type * as React from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export const CARD = "rounded-2xl bg-surface-raised shadow-soft ring-1 ring-border";

/** One screen of the flow: a single narrow column, on a phone and on a desk. */
export function Screen({ children, className, ...props }: React.ComponentProps<"div">) {
  return (
    <div className={cn("mx-auto w-full max-w-xl space-y-6", className)} {...props}>
      {children}
    </div>
  );
}

export function ScreenTitle({ title, line, eyebrow }: { title: string; line?: React.ReactNode; eyebrow?: string }) {
  return (
    <div className="space-y-1.5">
      {eyebrow ? <p className="text-[13.5px] font-medium text-muted-foreground">{eyebrow}</p> : null}
      <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">{title}</h1>
      {line ? <p className="text-[15px] text-muted-foreground text-pretty">{line}</p> : null}
    </div>
  );
}

/** A full-width button a thumb cannot miss. */
export function BigButton({ className, ...props }: ButtonProps) {
  return <Button size="lg" className={cn("h-14 w-full rounded-2xl px-5 text-[16px] [&_svg]:size-5", className)} {...props} />;
}

export function BackButton({ onClick, children = "Back" }: { onClick: () => void; children?: React.ReactNode }) {
  return (
    <Button variant="ghost" size="lg" className="-ml-3 text-[15px]" onClick={onClick}>
      <ArrowLeft aria-hidden />
      {children}
    </Button>
  );
}

/** An answer to a question: a tap picks it and moves on. */
export function Choice({ label, hint, picked = false, onPick }: { label: string; hint?: string; picked?: boolean; onPick: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={picked}
      onClick={onPick}
      className={cn(
        CARD,
        "flex min-h-16 w-full cursor-pointer items-center gap-3 px-5 py-3.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-brand",
        picked ? "ring-2 ring-brand" : "hover:ring-foreground/25",
      )}
    >
      <span className="min-w-0 flex-1">
        <span className="block text-[17px] font-semibold tracking-[-0.01em]">{label}</span>
        {hint ? <span className="block text-[14px] text-muted-foreground">{hint}</span> : null}
      </span>
      {picked ? <span className="shrink-0 text-[13px] font-medium text-brand">Picked</span> : null}
      <ChevronRight aria-hidden className="size-5 shrink-0 text-subtle-foreground" />
    </button>
  );
}

/** What went wrong, in a sentence, and the one thing to do next (the children: a button or a link). */
export function Problem({ title, children, message, className }: { title: string; message?: React.ReactNode; children?: React.ReactNode; className?: string }) {
  return (
    <section role="alert" className={cn(CARD, "space-y-4 p-5", className)}>
      <div className="flex items-start gap-3">
        <CircleAlert aria-hidden className="mt-0.5 size-6 shrink-0 text-foreground" />
        <div className="min-w-0 space-y-1">
          <h2 className="text-[17px] font-semibold tracking-[-0.01em] text-balance">{title}</h2>
          {message ? <p className="text-[15px] text-muted-foreground text-pretty">{message}</p> : null}
        </div>
      </div>
      {children}
    </section>
  );
}
