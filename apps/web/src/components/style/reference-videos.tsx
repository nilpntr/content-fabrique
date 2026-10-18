import { CircleAlert, Film, Loader2, Plus, X } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { notify } from "@/components/ui/toast";
import type { CreatorStyle, StyleReference } from "@/lib/api/client";
import { referenceLimitsLine, referenceStatus, referencesLine } from "@/lib/style-words";
import { useUploadCommands, useUploadSelector } from "@/lib/upload/provider";
import { cn } from "@/lib/utils";

/** The file types a style video may be (the API takes these three). */
export const REFERENCE_ACCEPT = "video/mp4,video/quicktime,video/webm,.mp4,.mov,.webm";
const VIDEO = /^video\/(mp4|quicktime|webm)$/;
const VIDEO_EXTENSION = /\.(mp4|mov|webm)$/i;

/**
 * Which of the picked files can be style videos: a video, not too big, and no more than there is room for. The rest are told about
 * with a reason.
 */
export function pickReferences(files: readonly File[], style: Pick<CreatorStyle, "references" | "limits">): { accepted: File[]; rejected: { name: string; reason: string }[] } {
  const room = Math.max(style.limits.max_references - style.references.filter((r) => r.status !== "failed").length, 0);
  const accepted: File[] = [];
  const rejected: { name: string; reason: string }[] = [];
  for (const file of files) {
    if (!(VIDEO.test(file.type) || (file.type === "" && VIDEO_EXTENSION.test(file.name)))) {
      rejected.push({ name: file.name, reason: "Only videos (MP4, MOV or WebM) can teach your style." });
    } else if (file.size > style.limits.max_size_bytes) {
      rejected.push({ name: file.name, reason: "This video is too big for a style video." });
    } else if (accepted.length >= room) {
      rejected.push({ name: file.name, reason: `You can have ${style.limits.max_references} style videos at most.` });
    } else {
      accepted.push(file);
    }
  }
  return { accepted, rejected };
}

/** The upload progress of the style videos this browser is sending, by asset id (0 to 1). */
function useReferenceProgress(): ReadonlyMap<string, number> {
  return useUploadSelector(
    (snapshot) => {
      const out = new Map<string, number>();
      for (const item of snapshot.items) if (item.purpose === "style_reference" && item.assetId) out.set(item.assetId, item.progress);
      return out;
    },
    (a, b) => a.size === b.size && [...a].every(([k, v]) => b.get(k) === v),
  );
}

/**
 * The creator's style videos: a still of each with how far it is ("Learning from it…", "Learnt"), a button to remove one and one to add
 * more. Adding uploads the files through the normal upload queue as style videos.
 */
export function ReferenceVideos({
  style,
  onRemove,
  removing,
  compact = false,
}: {
  style: CreatorStyle;
  onRemove?: (assetId: string) => void;
  /** The asset being removed, to show it as busy. */
  removing?: string | null;
  /** Fewer words, for the onboarding step. */
  compact?: boolean;
}) {
  const { addFiles, available } = useUploadCommands();
  const input = React.useRef<HTMLInputElement>(null);
  const progress = useReferenceProgress();
  const full = style.references.filter((r) => r.status !== "failed").length >= style.limits.max_references;

  async function add(files: File[]) {
    const { accepted, rejected } = pickReferences(files, style);
    if (rejected.length > 0) {
      const first = rejected[0];
      notify(rejected.length === 1 ? `“${first?.name}” was not added` : `${rejected.length} files were not added`, { description: first?.reason, type: "error" });
    }
    if (accepted.length > 0) await addFiles(accepted, { purpose: "style_reference" });
  }

  return (
    <div className="space-y-3">
      {style.references.length > 0 ? (
        <ul className="grid grid-cols-3 gap-2 sm:grid-cols-4 lg:grid-cols-5">
          {style.references.map((ref, index) => (
            <li key={ref.asset_id ?? `${ref.name}-${index}`}>
              <ReferenceCard reference={ref} progress={ref.asset_id ? progress.get(ref.asset_id) : undefined} onRemove={onRemove} busy={removing != null && removing === ref.asset_id} />
            </li>
          ))}
        </ul>
      ) : null}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button type="button" variant={style.references.length === 0 ? "default" : "secondary"} disabled={!available || full} onClick={() => input.current?.click()}>
          <Plus aria-hidden />
          {style.references.length === 0 ? "Add your videos" : "Add more"}
        </Button>
        <p className="text-[13px] text-muted-foreground">
          {referencesLine(style)}
          {compact ? null : ` ${referenceLimitsLine(style)}`}
        </p>
      </div>
      <input
        ref={input}
        type="file"
        multiple
        accept={REFERENCE_ACCEPT}
        tabIndex={-1}
        aria-hidden="true"
        data-testid="style-upload-input"
        className="hidden"
        onChange={(event) => {
          const files = [...(event.target.files ?? [])];
          event.target.value = "";
          if (files.length > 0) void add(files);
        }}
      />
    </div>
  );
}

function ReferenceCard({ reference, progress, onRemove, busy }: { reference: StyleReference; progress?: number; onRemove?: (assetId: string) => void; busy: boolean }) {
  const failed = reference.status === "failed";
  const working = reference.status === "uploading" || reference.status === "processing" || reference.status === "analysing";
  const status = reference.status === "uploading" && progress !== undefined ? `Uploading… ${Math.round(progress * 100)} %` : referenceStatus(reference);
  return (
    <figure className={cn("group/ref relative overflow-hidden rounded-xl bg-well shadow-well ring-1 ring-border", busy && "opacity-50")}>
      <div className="relative aspect-[9/14] w-full">
        {reference.poster_url ? (
          <img src={reference.poster_url} alt="" className="absolute inset-0 size-full object-cover" loading="lazy" />
        ) : (
          <div className="absolute inset-0 flex items-center justify-center text-subtle-foreground">
            <Film aria-hidden className="size-6" />
          </div>
        )}
        {working ? (
          <div className="absolute inset-x-0 bottom-0 h-1 bg-black/20">
            <div className="h-full bg-brand transition-[width] duration-(--dur-base)" style={{ width: `${Math.round((reference.status === "uploading" ? (progress ?? 0) : reference.status === "processing" ? reference.progress / 100 : 1) * 100)}%` }} />
          </div>
        ) : null}
        {onRemove && reference.asset_id ? (
          <button
            type="button"
            disabled={busy}
            onClick={() => reference.asset_id && onRemove(reference.asset_id)}
            aria-label={`Remove ${reference.name}`}
            className="absolute top-1.5 right-1.5 flex size-7 cursor-pointer items-center justify-center rounded-full bg-black/55 text-white backdrop-blur-sm transition-opacity hover:bg-black/70 focus-visible:ring-2 focus-visible:ring-brand disabled:cursor-default"
          >
            <X aria-hidden className="size-3.5" />
          </button>
        ) : null}
      </div>
      <figcaption className="space-y-0.5 px-2 py-1.5">
        <p className="truncate text-[12px] font-medium" title={reference.name}>
          {reference.name}
        </p>
        <p className={cn("flex items-center gap-1 text-[11.5px] leading-tight", failed ? "text-destructive" : "text-muted-foreground")}>
          {working ? <Loader2 aria-hidden className="size-3 shrink-0 animate-spin motion-reduce:animate-none" /> : failed ? <CircleAlert aria-hidden className="size-3 shrink-0" /> : null}
          <span className="line-clamp-2">{status}</span>
        </p>
      </figcaption>
    </figure>
  );
}
