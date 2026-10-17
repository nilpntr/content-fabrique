import { CornerDownRight, MapPin, MessageSquare } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { cn, hue, initials } from "@/lib/utils";
import { type CommentFilter, type CommentStatus, countOpen, filterComments, type ResolvedComment, statusLabel } from "./lib/comments";
import { formatRange } from "./lib/time";

export interface CommentPanelProps {
  items: ResolvedComment[];
  selectedId: string | null;
  hoveredId: string | null;
  onSelect: (item: ResolvedComment) => void;
  onHover: (id: string | null) => void;
  /** For a comment that no longer maps: show the version it was made on. */
  onOpenVersion: (version: number, commentId: string) => void;
  className?: string;
}

function CommentPanelView({ items, selectedId, hoveredId, onSelect, onHover, onOpenVersion, className }: CommentPanelProps) {
  const [filter, setFilter] = React.useState<CommentFilter>("all");
  const visible = React.useMemo(() => filterComments(items, filter), [items, filter]);
  const open = countOpen(items);
  const total = items.filter((item) => !item.isReply).length;
  const listRef = React.useRef<HTMLUListElement>(null);

  // A comment picked on the timeline scrolls into view in the list.
  React.useEffect(() => {
    if (!selectedId) return;
    const el = [...(listRef.current?.querySelectorAll<HTMLElement>("[data-comment-id]") ?? [])].find((node) => node.dataset.commentId === selectedId);
    el?.scrollIntoView?.({ block: "nearest" });
  }, [selectedId]);

  return (
    <aside
      aria-label="Comment history"
      className={cn("flex min-h-0 flex-col rounded-xl bg-surface-raised shadow-soft ring-1 ring-border", className)}
    >
      <div className="flex items-center gap-2 border-b border-hairline px-3 py-2.5">
        <MessageSquare className="size-4 text-muted-foreground" aria-hidden />
        <h2 className="text-[13px] font-semibold tracking-[-0.01em]">Comments</h2>
        <span className="font-mono text-[12px] tabular-nums text-muted-foreground">
          {open} open · {total} total
        </span>
        <fieldset className="ml-auto flex rounded-[8px] bg-wash p-0.5">
          <legend className="sr-only">Filter comments</legend>
          {(["open", "all"] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={filter === value}
              onClick={() => setFilter(value)}
              className={cn(
                "h-6 cursor-pointer rounded-[6px] px-2.5 text-[12px] font-medium text-muted-foreground transition-colors outline-none focus-visible:ring-2 focus-visible:ring-brand",
                filter === value && "bg-surface-raised text-foreground shadow-raised",
              )}
            >
              {value === "open" ? "Open" : "All"}
            </button>
          ))}
        </fieldset>
      </div>

      {visible.length === 0 ? (
        <p className="px-4 py-8 text-center text-[13px] text-muted-foreground text-pretty">
          {filter === "open" ? "No open comments on this version." : "No comments yet. Drag across the timeline or pin the frame to add one."}
        </p>
      ) : (
        <ul ref={listRef} className="min-h-0 flex-1 space-y-1 overflow-y-auto p-1.5">
          {visible.map((item) => (
            <CommentItem
              key={item.comment.id}
              item={item}
              selected={item.comment.id === selectedId}
              hovered={item.comment.id === hoveredId}
              onSelect={onSelect}
              onHover={onHover}
              onOpenVersion={onOpenVersion}
            />
          ))}
        </ul>
      )}
    </aside>
  );
}

const CommentItem = React.memo(function CommentItem({
  item,
  selected,
  hovered,
  onSelect,
  onHover,
  onOpenVersion,
}: {
  item: ResolvedComment;
  selected: boolean;
  hovered: boolean;
  onSelect: (item: ResolvedComment) => void;
  onHover: (id: string | null) => void;
  onOpenVersion: (version: number, commentId: string) => void;
}) {
  const { comment, mapped, status } = item;
  const shown = mapped ?? comment;
  const moved = mapped !== null && (mapped.start_ms !== comment.start_ms || mapped.end_ms !== comment.end_ms);
  const isAgent = comment.author.kind === "agent";

  return (
    <li
      data-comment-id={comment.id}
      data-selected={selected}
      data-hovered={hovered}
      data-status={status.kind}
      onPointerEnter={() => onHover(comment.id)}
      onPointerLeave={() => onHover(null)}
      className={cn(
        "rounded-[10px] transition-colors",
        item.isReply && "ml-5",
        "data-[hovered=true]:bg-wash data-[selected=true]:bg-brand-wash data-[selected=true]:shadow-[inset_0_0_0_1px_var(--brand)]",
        (status.kind === "unmapped" || status.kind === "newer") && "opacity-85",
      )}
    >
      <button
        type="button"
        onClick={() => onSelect(item)}
        onFocus={() => onHover(comment.id)}
        onBlur={() => onHover(null)}
        className="block w-full cursor-pointer rounded-[10px] px-2.5 pt-2 pb-1.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-brand"
      >
        <span className="flex items-center gap-1.5">
          {item.isReply && <CornerDownRight className="size-3.5 text-muted-foreground" aria-hidden />}
          <span
            className={cn("font-mono text-[12px] tabular-nums", status.kind === "unmapped" ? "text-muted-foreground line-through decoration-1" : "text-foreground")}
          >
            {formatRange(shown.start_ms, shown.end_ms)}
          </span>
          {comment.pin && <MapPin className="size-3.5 text-brand" aria-label="Pinned on the frame" />}
          {!item.isReply && <StatusChip status={status} />}
        </span>
        <span className="mt-1 block text-[13px] leading-snug text-pretty text-foreground">{comment.body}</span>
        <span className="mt-1.5 flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
          <Avatar name={comment.author.name} />
          <span className="font-medium">{comment.author.name}</span>
          {isAgent && <span className="rounded-[4px] bg-wash px-1 py-px text-[10.5px] font-medium">Agent reply</span>}
          <span aria-hidden>·</span>
          <span>made on v{comment.version}</span>
        </span>
      </button>

      {(moved || item.addressedIn !== undefined || status.kind === "unmapped" || status.kind === "newer") && !item.isReply && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-2.5 pb-2 text-[11.5px] text-muted-foreground">
          {(moved || status.kind === "unmapped") && (
            <span>
              Was <span className="font-mono tabular-nums">{formatRange(comment.start_ms, comment.end_ms)}</span> on v{comment.version}
            </span>
          )}
          {item.addressedIn !== undefined && <span>Addressed in v{item.addressedIn}</span>}
          {(status.kind === "unmapped" || status.kind === "newer") && (
            <Button variant="link" size="sm" className="h-auto p-0 text-[11.5px]" onClick={() => onOpenVersion(comment.version, comment.id)}>
              Open v{comment.version}
            </Button>
          )}
        </div>
      )}
    </li>
  );
});

export const CommentPanel = React.memo(CommentPanelView);

function StatusChip({ status }: { status: CommentStatus }) {
  return (
    <span
      data-status-chip={status.kind}
      className={cn(
        "ml-auto rounded-full px-2 py-px text-[11px] leading-4 font-medium",
        status.kind === "open" && "bg-brand-wash text-brand",
        status.kind === "addressed" && "bg-add-wash text-good",
        (status.kind === "applied" || status.kind === "resolved" || status.kind === "unmapped" || status.kind === "newer") &&
          "bg-wash text-muted-foreground",
      )}
    >
      {statusLabel(status)}
    </span>
  );
}

function Avatar({ name }: { name: string }) {
  return (
    <span
      aria-hidden
      className="grid size-4 place-items-center rounded-full text-[8.5px] font-semibold text-white"
      style={{ background: `hsl(${hue(name)} 45% 42%)` }}
    >
      {initials(name)}
    </span>
  );
}
