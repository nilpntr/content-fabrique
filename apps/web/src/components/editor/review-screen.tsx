import { FolderOpen, X } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { useHotkey } from "@/hooks/use-hotkey";
import { cn } from "@/lib/utils";
import { CommentPanel } from "./comment-panel";
import { type ResolvedComment, resolveComments } from "./lib/comments";
import { markIn, markOut } from "./lib/selection";
import { type FramePin, PreviewPlayer } from "./preview-player";
import { ReviewTimeline, type TimelineRegion } from "./review-timeline";
import type { CommentDraft, Pin, ReviewAnnotation, ReviewVersion } from "./types";
import { usePlayerController } from "./use-player";
import { VersionSelector } from "./version-selector";

/** How far J and L jump. */
const JUMP_MS = 5000;

export interface ReviewScreenProps {
  versions: ReviewVersion[];
  /** The comments to start with. New ones are added to the list here and reported through `onCommentCreate`. */
  comments: ReviewAnnotation[];
  /** Names for the filmstrip and the placeholder, by `asset_id`. */
  assetNames?: Record<string, string>;
  /** A video to play for every version instead of the generated placeholder (a version's own `preview_url` wins). */
  src?: string;
  /** The version to open on. Defaults to the latest. */
  initialVersion?: number;
  onCommentCreate?: (comment: ReviewAnnotation) => void;
  /** Called when the creator moves the playhead: scrubbing, stepping, clicking a comment. Not on playback progress. */
  onSeek?: (ms: number) => void;
  className?: string;
}

export function ReviewScreen({ versions, comments: initialComments, assetNames, src, initialVersion, onCommentCreate, onSeek, className }: ReviewScreenProps) {
  const latest = Math.max(...versions.map((version) => version.version));
  const [versionNumber, setVersionNumber] = React.useState(initialVersion ?? latest);
  const version = versions.find((candidate) => candidate.version === versionNumber) ?? (versions[versions.length - 1] as ReviewVersion);

  const [comments, setComments] = React.useState(initialComments);
  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [hoveredId, setHoveredId] = React.useState<string | null>(null);
  const [draft, setDraft] = React.useState<CommentDraft | null>(null);
  const [composing, setComposing] = React.useState(false);
  const [pinMode, setPinMode] = React.useState(false);
  const [localSrc, setLocalSrc] = React.useState<{ url: string; name: string } | null>(null);
  const created = React.useRef(0);
  const fileInput = React.useRef<HTMLInputElement>(null);

  const player = usePlayerController({ durationMs: version.duration_ms, fps: version.output.fps, onSeek });
  const videoSrc = localSrc?.url ?? version.preview_url ?? src;

  // An object URL lives until the page is closed or we let it go.
  React.useEffect(() => () => (localSrc ? URL.revokeObjectURL(localSrc.url) : undefined), [localSrc]);

  const resolved = React.useMemo(() => resolveComments(comments, versions, version), [comments, versions, version]);
  const byId = React.useMemo(() => new Map(resolved.map((item) => [item.comment.id, item])), [resolved]);
  const selected = selectedId ? byId.get(selectedId) : undefined;
  const hovered = hoveredId ? byId.get(hoveredId) : undefined;

  const regions = React.useMemo<TimelineRegion[]>(
    () =>
      resolved.flatMap((item) =>
        item.isReply || !item.mapped
          ? []
          : [
              {
                id: item.comment.id,
                start_ms: item.mapped.start_ms,
                end_ms: item.mapped.end_ms,
                body: item.comment.body,
                tone: item.status.kind === "addressed" ? ("addressed" as const) : ("open" as const),
                pinned: item.comment.pin !== undefined,
              },
            ],
      ),
    [resolved],
  );

  const pins: FramePin[] = [];
  if (draft?.pin) pins.push({ pin: draft.pin, tone: "draft" });
  const selectedPin = selected?.mapped ? byId.get(selected.rootId)?.comment.pin : undefined;
  if (selectedPin) pins.push({ pin: selectedPin, tone: "selected" });

  const clearDraft = () => {
    setDraft(null);
    setComposing(false);
  };

  const selectComment = (item: ResolvedComment) => {
    clearDraft();
    setPinMode(false);
    setSelectedId(item.comment.id);
    if (!item.mapped) return;
    player.pause();
    player.seek(item.mapped.start_ms);
  };

  const selectVersion = (next: number) => {
    player.pause();
    clearDraft();
    setVersionNumber(next);
  };

  const saveDraft = (body: string) => {
    if (!draft) return;
    created.current += 1;
    const comment: ReviewAnnotation = {
      // Placeholder until the annotations API issues a TypeID.
      id: `ann_local_${created.current}`,
      version: version.version,
      start_ms: draft.start_ms,
      end_ms: draft.end_ms,
      ...(draft.pin ? { pin: draft.pin } : {}),
      kind: "comment",
      body,
      status: "open",
      author: { kind: "creator", name: "You" },
      created_at: new Date().toISOString(),
    };
    setComments((current) => [...current, comment]);
    setSelectedId(comment.id);
    clearDraft();
    onCommentCreate?.(comment);
  };

  const changeDraft = (next: CommentDraft | null, options?: { compose?: boolean }) => {
    setDraft(next);
    if (options?.compose !== undefined) setComposing(options.compose && next !== null);
    if (next && options?.compose) {
      setSelectedId(null);
      setPinMode(false);
    }
  };

  const placePin = (pin: Pin) => {
    player.pause();
    const at = player.currentMs;
    setDraft({ start_ms: draft?.start_ms ?? at, end_ms: draft?.end_ms ?? at, pin });
    setComposing(true);
    setSelectedId(null);
    setPinMode(false);
  };

  // --- Keyboard. The hook skips bare keys while a text field has focus, so typing a comment never moves the player.
  useHotkey(" ", (event) => {
    // On a button, Space presses that button.
    if (!(event.target instanceof HTMLButtonElement)) player.toggle();
  });
  useHotkey("k", player.toggle);
  useHotkey("j", () => player.skip(-JUMP_MS));
  useHotkey("l", () => player.skip(JUMP_MS));
  useHotkey(",", () => player.step(-1));
  useHotkey(".", () => player.step(1));
  useHotkey("p", () => setPinMode((on) => !on));

  const mark = (fn: typeof markIn) => {
    const range = draft && draft.end_ms > draft.start_ms ? draft : null;
    setDraft({ ...fn(range, player.currentMs, version.duration_ms), ...(draft?.pin ? { pin: draft.pin } : {}) });
    setComposing(false);
    setSelectedId(null);
  };
  useHotkey("i", () => mark(markIn));
  useHotkey("o", () => mark(markOut));
  useHotkey("c", () => {
    if (!draft) setDraft({ start_ms: player.currentMs, end_ms: player.currentMs });
    setComposing(true);
    setSelectedId(null);
  });
  useHotkey("Escape", () => {
    if (draft) clearDraft();
    else if (pinMode) setPinMode(false);
    else setSelectedId(null);
  });

  const pickFile = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (file) setLocalSrc({ url: URL.createObjectURL(file), name: file.name });
  };

  return (
    <section aria-label="Review" className={cn("grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start", className)}>
      <div className="min-w-0 space-y-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <VersionSelector versions={versions} selected={version.version} onSelect={selectVersion} className="min-w-0 flex-1" />
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              title="Play a video from your computer instead of the placeholder"
              onClick={() => fileInput.current?.click()}
            >
              <FolderOpen />
              {localSrc ? <span className="max-w-32 truncate">{localSrc.name}</span> : "Local video"}
            </Button>
            <input ref={fileInput} type="file" accept="video/*" hidden aria-label="Choose a local video file" onChange={pickFile} />
            {localSrc && (
              <Button variant="ghost" size="icon-sm" aria-label="Use the placeholder again" onClick={() => setLocalSrc(null)}>
                <X />
              </Button>
            )}
          </div>
        </div>

        {version.version !== latest && (
          <p className="rounded-[10px] bg-wash px-3 py-2 text-[12.5px] text-muted-foreground">
            You are looking at v{version.version}, not the latest version. Comments made on other versions are mapped onto it.
          </p>
        )}

        <PreviewPlayer
          player={player}
          version={version}
          src={videoSrc}
          assetNames={assetNames}
          pinMode={pinMode}
          onPinModeChange={setPinMode}
          onPlacePin={placePin}
          pins={pins}
        />

        <ReviewTimeline
          version={version}
          player={player}
          assetNames={assetNames}
          regions={regions}
          selectedRegionId={selected?.rootId ?? null}
          hoveredRegionId={hovered?.rootId ?? null}
          draft={draft}
          composing={composing}
          onDraftChange={changeDraft}
          onSaveDraft={saveDraft}
          onCancelDraft={clearDraft}
          onSelectRegion={(id) => {
            const item = byId.get(id);
            if (item) selectComment(item);
          }}
          onHoverRegion={setHoveredId}
        />

        <ShortcutLegend />
      </div>

      <CommentPanel
        items={resolved}
        selectedId={selectedId}
        hoveredId={hoveredId}
        onSelect={selectComment}
        onHover={setHoveredId}
        onOpenVersion={(target, commentId) => {
          selectVersion(target);
          setSelectedId(commentId);
        }}
        className="lg:sticky lg:top-4 lg:max-h-[calc(100svh-2rem)]"
      />
    </section>
  );
}

const SHORTCUTS: [string[], string][] = [
  [["Space", "K"], "play"],
  [["J", "L"], "±5 s"],
  [[",", "."], "frame"],
  [["I", "O"], "in, out"],
  [["C"], "comment"],
  [["P"], "pin"],
  [["Esc"], "cancel"],
];

function ShortcutLegend() {
  return (
    <ul aria-label="Keyboard shortcuts" className="hidden flex-wrap gap-x-4 gap-y-1.5 text-[12px] text-muted-foreground sm:flex">
      {SHORTCUTS.map(([keys, label]) => (
        <li key={label} className="flex items-center gap-1">
          {keys.map((key) => (
            <Kbd key={key}>{key}</Kbd>
          ))}
          <span>{label}</span>
        </li>
      ))}
    </ul>
  );
}
