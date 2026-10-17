import { ChevronDown, RotateCw, X } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { formatBytes, formatEta, formatRate } from "@/lib/upload/format";
import type { UploadItem, UploadSummary } from "@/lib/upload/manager";
import { useUploads } from "@/lib/upload/provider";
import { cn } from "@/lib/utils";
import { UploadButton } from "./upload-button";

const RANK: Record<UploadItem["status"], number> = { uploading: 0, completing: 0, starting: 0, queued: 1, "needs-file": 2, failed: 3, done: 4 };

/** What a row says under the file's name. */
export function itemText(item: UploadItem): string {
  const percent = Math.round(item.progress * 100);
  switch (item.status) {
    case "queued":
      return item.note ?? "Waiting for its turn";
    case "starting":
      return item.note ?? "Starting";
    case "uploading": {
      const parts = [`${formatBytes(item.bytesDone)} of ${formatBytes(item.size)}`];
      if (item.speed > 0) parts.push(formatRate(item.speed));
      if (item.etaSeconds !== null) parts.push(`${formatEta(item.etaSeconds)} left`);
      if (item.note) parts.push(item.note);
      return parts.join(" · ");
    }
    case "completing":
      return item.note ?? "Finishing";
    case "done":
      return "Uploaded";
    case "failed":
      return item.error?.message ?? "The upload failed";
    case "needs-file":
      return percent > 0 ? `Choose this file again to continue (${percent}% was uploaded)` : "Choose this file again to continue";
  }
}

/** The header line of the tray. */
export function summaryText(summary: UploadSummary): string {
  const unfinished = summary.active + summary.queued;
  if (unfinished > 0) {
    const parts = [`Uploading ${summary.done} of ${summary.total}`];
    if (summary.speed > 0) parts.push(formatRate(summary.speed));
    if (summary.etaSeconds !== null) parts.push(`${formatEta(summary.etaSeconds)} left`);
    return parts.join(" · ");
  }
  if (summary.needsFile > 0) return `${summary.needsFile} ${summary.needsFile === 1 ? "upload needs its" : "uploads need their"} ${summary.needsFile === 1 ? "file" : "files"}`;
  if (summary.failed > 0) return `${summary.failed} ${summary.failed === 1 ? "upload" : "uploads"} failed`;
  return `${summary.done} ${summary.done === 1 ? "upload" : "uploads"} finished`;
}

/** Asks for the files of the uploads that a reload interrupted. */
export function ResumePrompt({ className }: { className?: string }) {
  const { snapshot } = useUploads();
  const count = snapshot.summary.needsFile;
  if (count === 0) return null;
  return (
    <div role="alert" data-slot="resume-prompt" className={cn("flex flex-wrap items-center justify-between gap-3 rounded-xl bg-brand-wash px-3 py-2.5 text-[13px]", className)}>
      <p className="min-w-0 text-pretty">
        <span className="font-medium">{count === 1 ? "1 upload was interrupted." : `${count} uploads were interrupted.`}</span> Choose the same {count === 1 ? "file" : "files"} again and{" "}
        {count === 1 ? "it continues" : "they continue"} where {count === 1 ? "it" : "they"} stopped.
      </p>
      <UploadButton size="sm">Choose files</UploadButton>
    </div>
  );
}

function Row({ item, onCancel, onRetry }: { item: UploadItem; onCancel: () => void; onRetry: () => void }) {
  const percent = Math.round(item.progress * 100);
  const text = itemText(item);
  const failed = item.status === "failed";
  return (
    <li className="flex items-start gap-2 px-3 py-2" data-status={item.status}>
      <div className="min-w-0 flex-1 space-y-1">
        <p className="truncate text-[13px] font-medium" title={item.name}>
          {item.name}
        </p>
        <div
          role="progressbar"
          aria-label={`Upload of ${item.name}`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          aria-valuetext={`${percent}%. ${text}`}
          className="h-1 overflow-hidden rounded-full bg-wash"
        >
          <div
            className={cn("h-full rounded-full transition-[width] duration-(--dur-slow) ease-out", failed ? "bg-destructive" : item.status === "done" ? "bg-good" : "bg-brand")}
            style={{ width: `${percent}%` }}
          />
        </div>
        <p className={cn("text-[12px] tabular-nums", failed ? "text-destructive" : "text-muted-foreground")} aria-hidden="true">
          {text}
        </p>
      </div>
      {failed ? (
        <Button variant="ghost" size="icon-sm" aria-label={`Retry upload of ${item.name}`} onClick={onRetry}>
          <RotateCw aria-hidden="true" />
        </Button>
      ) : null}
      <Button variant="ghost" size="icon-sm" aria-label={item.status === "done" ? `Dismiss ${item.name}` : `Cancel upload of ${item.name}`} onClick={onCancel}>
        <X aria-hidden="true" />
      </Button>
    </li>
  );
}

/**
 * The uploads, wherever you are in the app: progress per file, speed and time left, retry and cancel, and the prompt for
 * the files of uploads that a reload interrupted. The list is collapsible; its header is a button, and the progress of
 * every file is a progressbar with a text alternative. Screen readers hear one polite line when a file finishes or fails,
 * not every percent.
 */
export function UploadsTray() {
  const { snapshot, cancel, retry, retryAllFailed, clearDone } = useUploads();
  const [open, setOpen] = React.useState(true);
  const listId = React.useId();
  const { items, summary } = snapshot;
  const sorted = React.useMemo(() => [...items].sort((a, b) => RANK[a.status] - RANK[b.status]), [items]);
  if (items.length === 0) return null;

  const percent = summary.bytesTotal > 0 ? Math.round((summary.bytesDone / summary.bytesTotal) * 100) : 0;
  const announcement = `${summary.done} of ${summary.total} uploads finished${summary.failed > 0 ? `, ${summary.failed} failed` : ""}${summary.needsFile > 0 ? `, ${summary.needsFile} waiting for their files` : ""}`;

  return (
    <section
      aria-label="Uploads"
      data-slot="uploads-tray"
      className="fixed right-4 bottom-4 z-50 w-[min(24rem,calc(100vw-2rem))] overflow-hidden rounded-2xl bg-surface-raised shadow-soft ring-1 ring-border sm:right-6 sm:bottom-6"
    >
      <p role="status" className="sr-only">
        {announcement}
      </p>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        onClick={() => setOpen((value) => !value)}
        className="flex w-full cursor-pointer items-center gap-2 px-3 py-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset"
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13.5px] font-medium tabular-nums">{summaryText(summary)}</span>
          {summary.active + summary.queued > 0 ? (
            <span aria-hidden="true" className="mt-1.5 block h-1 overflow-hidden rounded-full bg-wash">
              <span className="block h-full rounded-full bg-brand transition-[width] duration-(--dur-slow) ease-out" style={{ width: `${percent}%` }} />
            </span>
          ) : null}
        </span>
        <ChevronDown aria-hidden="true" className={cn("size-4 shrink-0 text-subtle-foreground transition-transform duration-(--dur-base)", !open && "-rotate-180")} />
      </button>
      <div id={listId} hidden={!open}>
        <ResumePrompt className="mx-3 mb-2" />
        <ul className="max-h-72 divide-y divide-hairline overflow-y-auto border-t border-hairline">
          {sorted.map((item) => (
            <Row key={item.id} item={item} onCancel={() => cancel(item.id)} onRetry={() => retry(item.id)} />
          ))}
        </ul>
        {summary.failed > 0 || summary.done > 0 ? (
          <div className="flex justify-end gap-2 border-t border-hairline px-3 py-2">
            {summary.failed > 1 ? (
              <Button variant="secondary" size="sm" onClick={retryAllFailed}>
                Retry all failed
              </Button>
            ) : null}
            {summary.done > 0 ? (
              <Button variant="ghost" size="sm" onClick={clearDone}>
                Clear finished
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </section>
  );
}
