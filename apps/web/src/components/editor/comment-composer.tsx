import { MapPin } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { useModKey } from "@/hooks/use-hotkey";
import { cn } from "@/lib/utils";
import { formatRange } from "./lib/time";
import type { CommentDraft } from "./types";

export interface CommentComposerProps {
  draft: CommentDraft;
  /** The text typed so far. The screen owns it, so it stays with this comment's range. */
  body: string;
  onBodyChange: (body: string) => void;
  /** A line under the field, e.g. why a new selection was ignored. */
  notice?: { text: string };
  onSave: (body: string) => void;
  onCancel: () => void;
  className?: string;
  style?: React.CSSProperties;
}

/** The popover under the timeline where the creator types a comment on the selected range, point or pin. */
export function CommentComposer({ draft, body, onBodyChange, notice, onSave, onCancel, className, style }: CommentComposerProps) {
  const root = React.useRef<HTMLDivElement>(null);
  const field = React.useRef<HTMLTextAreaElement>(null);
  const mod = useModKey();
  const trimmed = body.trim();

  // Under the timeline the box can be below the fold: bring it into view and put the cursor in it.
  // A notice means the creator tried to leave: bring them back to the text.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `notice` is the trigger, not a value the effect reads.
  React.useEffect(() => {
    root.current?.scrollIntoView?.({ block: "nearest" });
    field.current?.focus({ preventScroll: true });
  }, [notice]);

  const save = () => {
    if (trimmed) onSave(trimmed);
  };

  return (
    <div
      ref={root}
      role="dialog"
      aria-label="Add comment"
      className={cn("w-80 max-w-full rounded-xl bg-popover p-3 text-popover-foreground shadow-pop ring-1 ring-border", className)}
      style={style}
    >
      <p className="mb-2 flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
        <span>{draft.end_ms > draft.start_ms ? "Range" : "Point"}</span>
        <span className="font-mono tabular-nums text-foreground">{formatRange(draft.start_ms, draft.end_ms)}</span>
        {draft.pin && (
          <span className="inline-flex items-center gap-0.5 text-brand">
            <MapPin className="size-3.5" aria-hidden /> pin on frame
          </span>
        )}
      </p>
      <textarea
        ref={field}
        aria-label="Comment"
        rows={3}
        value={body}
        placeholder="What should change here?"
        onChange={(event) => onBodyChange(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onCancel();
          } else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
            event.preventDefault();
            save();
          }
        }}
        className="block w-full resize-none rounded-[10px] bg-well px-3 py-2 text-[13.5px] text-foreground shadow-well outline-none placeholder:text-subtle-foreground focus-visible:bg-surface-raised focus-visible:shadow-[inset_0_0_0_1px_var(--brand),0_0_0_3px_var(--brand-wash)]"
      />
      {notice && (
        <p role="status" className="mt-1.5 text-[12px] text-muted-foreground">
          {notice.text}
        </p>
      )}
      <div className="mt-2 flex items-center justify-end gap-1.5">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" disabled={!trimmed} onClick={save}>
          Save comment
          <Kbd aria-hidden className="ml-0.5 bg-white/15 bg-none text-white/80 shadow-none dark:bg-black/10 dark:text-black/60">{mod}↵</Kbd>
        </Button>
      </div>
    </div>
  );
}
