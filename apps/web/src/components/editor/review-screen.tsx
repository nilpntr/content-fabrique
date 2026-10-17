import { FolderOpen, Film, X } from "lucide-react";
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

/** Makes the placeholder ids of the comments this tab writes; they differ across screens so that two never collide. */
let localComments = 0;

/** How far J and L jump. */
const JUMP_MS = 5000;

const LOCKED_NOTICE = "Save or cancel this comment before starting another.";

export interface ReviewScreenProps {
  versions: ReviewVersion[];
  /** The comments to start with. New ones are added to the list here and reported through `onCommentCreate`. */
  comments: ReviewAnnotation[];
  /** Names for the filmstrip and the placeholder, by `asset_id`. */
  assetNames?: Record<string, string>;
  /** A video to play for every version instead of the generated placeholder (a version's own `preview_url` wins). */
  src?: string;
  /** The version to open on. Falls back to the latest version when it is not given or does not exist. */
  initialVersion?: number;
  /** A promise that resolves with `{ id }` (the id the API issued) keeps the new comment selected under that id. */
  onCommentCreate?: (comment: ReviewAnnotation) => unknown;
  /** Called with the whole list after every change to it (a comment added, edited, resolved or deleted). */
  onCommentsChange?: (comments: ReviewAnnotation[]) => void;
  /**
   * A comment was edited or resolved (`status`) in the panel. Giving it, with `onCommentDelete`, turns the actions on: Resolve and
   * Reopen for every comment, Edit and Delete for the ones marked `author.own`.
   */
  onCommentUpdate?: (id: string, patch: { body?: string; status?: "open" | "resolved" }) => void;
  onCommentDelete?: (id: string) => void;
  /** Why the last change could not be saved. The screen has already taken its own copy of the change back when `comments` says so. */
  commentsError?: string;
  onDismissCommentsError?: () => void;
  /** Whether the "Local video" button is offered (it plays a file from the computer instead of the placeholder). Default true. */
  localVideo?: boolean;
  /** What the frame says when there is no video to play. Default "Placeholder preview". */
  placeholderLabel?: string;
  /** Shown above the player: e.g. the state of the preview render. */
  aboveVideo?: (context: ReviewContext) => React.ReactNode;
  /** Shown under the timeline: e.g. the list of versions. */
  belowTimeline?: (context: ReviewContext) => React.ReactNode;
  /** Called with the version on screen: at the start, and whenever another is chosen. */
  onVersionChange?: (version: number) => void;
  /** Called when the video would not load (an expired URL, say). */
  onVideoError?: () => void;
  /** A line at the top of the comment panel, e.g. that comments are not saved. */
  commentsNote?: string;
  /** Called when the creator moves the playhead: scrubbing, stepping, clicking a comment. Not on playback progress. */
  onSeek?: (ms: number) => void;
  className?: string;
}

/** What the slots of the screen get to know about it. */
export interface ReviewContext {
  /** The version on screen. */
  version: ReviewVersion;
  latest: number;
  /** Shows another version (refused, with a notice, while a comment is being typed). */
  selectVersion: (version: number) => void;
}

export function ReviewScreen(props: ReviewScreenProps) {
  if (props.versions.length === 0) return <NoVersions className={props.className} />;
  return <ReviewWorkspace {...props} />;
}

function NoVersions({ className }: { className?: string }) {
  return (
    <section
      aria-label="Review"
      className={cn("grid place-items-center rounded-xl bg-surface-raised px-6 py-16 text-center shadow-soft ring-1 ring-border", className)}
    >
      <div className="max-w-sm space-y-2">
        <Film className="mx-auto size-6 text-muted-foreground" aria-hidden />
        <h2 className="text-[15px] font-semibold tracking-[-0.01em]">No versions to review yet</h2>
        <p className="text-[13px] text-muted-foreground text-pretty">
          Once the editor agent has rendered a preview, it shows up here and you can comment on it.
        </p>
      </div>
    </section>
  );
}

/** Whether Space belongs to the focused control (a button presses, a slider is operated) and not to the player. */
const spaceBelongsToTarget = (event: KeyboardEvent) =>
  event.target instanceof HTMLElement && event.target.matches('button, input, select, textarea, a[href], summary, [role="button"], [role="slider"]');

function ReviewWorkspace({
  versions,
  comments: initialComments,
  assetNames,
  src,
  initialVersion,
  onCommentCreate,
  onCommentsChange,
  onCommentUpdate,
  onCommentDelete,
  commentsError,
  onDismissCommentsError,
  onSeek,
  className,
  localVideo = true,
  placeholderLabel,
  aboveVideo,
  belowTimeline,
  onVersionChange,
  onVideoError,
  commentsNote,
}: ReviewScreenProps) {
  const latest = React.useMemo(() => Math.max(...versions.map((candidate) => candidate.version)), [versions]);
  const [versionNumber, setVersionNumber] = React.useState(() =>
    versions.some((candidate) => candidate.version === initialVersion) ? (initialVersion as number) : latest,
  );
  const version = versions.find((candidate) => candidate.version === versionNumber) ?? (versions.find((candidate) => candidate.version === latest) as ReviewVersion);

  const [comments, setComments] = React.useState(initialComments);
  const commentsRef = React.useRef(comments);
  // The comments the parent holds are the truth: when it hands over a new list (a comment was saved and got its id, a change was
  // refused and taken back, a teammate commented) the screen takes it. The screen's own changes show at once and the parent
  // follows in the same breath, so the two meet.
  const handedOver = React.useRef(initialComments);
  React.useEffect(() => {
    if (handedOver.current === initialComments) return;
    handedOver.current = initialComments;
    commentsRef.current = initialComments;
    setComments(initialComments);
  }, [initialComments]);
  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [hoveredId, setHoveredId] = React.useState<string | null>(null);
  const [draft, setDraft] = React.useState<CommentDraft | null>(null);
  const [draftBody, setDraftBody] = React.useState("");
  const [notice, setNotice] = React.useState<{ text: string } | undefined>();
  const [composing, setComposing] = React.useState(false);
  const [pinMode, setPinMode] = React.useState(false);
  const [localSrc, setLocalSrc] = React.useState<{ url: string; name: string } | null>(null);
  const fileInput = React.useRef<HTMLInputElement>(null);
  /** Set when "Open vN" switches version: the seek has to wait for the new version's duration. */
  const pendingSeek = React.useRef<{ version: number; ms: number } | null>(null);

  const versionChanged = React.useRef(onVersionChange);
  versionChanged.current = onVersionChange;
  const shownVersion = version.version;
  React.useEffect(() => {
    versionChanged.current?.(shownVersion);
  }, [shownVersion]);

  const player = usePlayerController({ durationMs: version.duration_ms, fps: version.output.fps, onSeek });
  const videoSrc = localSrc?.url ?? version.preview_url ?? src;

  // A comment with typed text keeps its range: nothing may start another selection until it is saved or cancelled.
  const locked = draft !== null && draftBody.trim() !== "";
  const refuse = React.useCallback(() => {
    setComposing(true);
    setNotice({ text: LOCKED_NOTICE });
  }, []);

  // An object URL lives until the page is closed or we let it go.
  React.useEffect(() => () => (localSrc ? URL.revokeObjectURL(localSrc.url) : undefined), [localSrc]);

  React.useEffect(() => {
    const pending = pendingSeek.current;
    if (!pending || pending.version !== version.version) return;
    pendingSeek.current = null;
    player.seek(pending.ms);
  }, [version.version, player]);

  const resolved = React.useMemo(() => resolveComments(comments, versions, version), [comments, versions, version]);
  const byId = React.useMemo(() => new Map(resolved.map((item) => [item.comment.id, item])), [resolved]);
  const selected = selectedId ? byId.get(selectedId) : undefined;
  const hovered = hoveredId ? byId.get(hoveredId) : undefined;

  const regions = React.useMemo<TimelineRegion[]>(
    () =>
      resolved.flatMap((item) => {
        if (item.isReply || !item.mapped) return [];
        const tone = item.status.kind === "open" ? "open" : item.status.kind === "addressed" ? "addressed" : "neutral";
        return [
          {
            id: item.comment.id,
            start_ms: item.mapped.start_ms,
            end_ms: item.mapped.end_ms,
            body: item.comment.body,
            tone,
            pinned: item.comment.pin !== undefined,
          } satisfies TimelineRegion,
        ];
      }),
    [resolved],
  );

  const pins: FramePin[] = [];
  if (draft?.pin) pins.push({ pin: draft.pin, tone: "draft" });
  const selectedPin = selected?.mapped ? byId.get(selected.rootId)?.comment.pin : undefined;
  if (selectedPin) pins.push({ pin: selectedPin, tone: "selected" });

  const clearDraft = React.useCallback(() => {
    setDraft(null);
    setComposing(false);
    setDraftBody("");
    setNotice(undefined);
  }, []);

  const selectComment = React.useCallback(
    (item: ResolvedComment) => {
      // Selecting only highlights and seeks, so a comment being typed is left alone.
      if (!locked) clearDraft();
      setPinMode(false);
      setSelectedId(item.comment.id);
      if (!item.mapped) return;
      player.pause();
      player.seek(item.mapped.start_ms);
    },
    [locked, clearDraft, player],
  );

  const selectRegion = React.useCallback(
    (id: string) => {
      const item = byId.get(id);
      if (item) selectComment(item);
    },
    [byId, selectComment],
  );

  const selectVersion = React.useCallback(
    (next: number) => {
      if (next === version.version) return;
      // The comment being typed belongs to a range on this version.
      if (locked) return refuse();
      player.pause();
      clearDraft();
      setVersionNumber(next);
    },
    [version.version, locked, refuse, player, clearDraft],
  );

  const openVersion = React.useCallback(
    (target: number, commentId: string) => {
      const item = byId.get(commentId);
      if (locked) return refuse();
      selectVersion(target);
      setSelectedId(commentId);
      // The comment's own range is on the version it was made on.
      if (item) pendingSeek.current = { version: target, ms: item.comment.start_ms };
    },
    [byId, locked, refuse, selectVersion],
  );

  const saveDraft = React.useCallback(
    (body: string) => {
      if (!draft) return;
      localComments += 1;
      const comment: ReviewAnnotation = {
        // Placeholder until the annotations API issues a TypeID.
        id: `ann_local_${localComments}`,
        version: version.version,
        start_ms: draft.start_ms,
        end_ms: draft.end_ms,
        ...(draft.pin ? { pin: draft.pin } : {}),
        kind: "comment",
        body,
        status: "open",
        author: { kind: "creator", name: "You", own: true },
        created_at: new Date().toISOString(),
      };
      const next = [...commentsRef.current, comment];
      commentsRef.current = next;
      setComments(next);
      onCommentsChange?.(next);
      setSelectedId(comment.id);
      clearDraft();
      const saved = onCommentCreate?.(comment);
      // The API gives the comment its own id: keep it selected.
      if (saved instanceof Promise) {
        void saved.then((result: unknown) => {
          const id = (result as { id?: unknown } | undefined)?.id;
          if (typeof id === "string") setSelectedId((current) => (current === comment.id ? id : current));
        });
      }
    },
    [draft, version.version, clearDraft, onCommentCreate, onCommentsChange],
  );

  const change = React.useCallback(
    (next: ReviewAnnotation[]) => {
      commentsRef.current = next;
      setComments(next);
      onCommentsChange?.(next);
    },
    [onCommentsChange],
  );

  const updateComment = React.useCallback(
    (id: string, patch: { body?: string; status?: "open" | "resolved" }) => {
      change(
        commentsRef.current.map((comment) => {
          if (comment.id !== id) return comment;
          // What the editor applied is not what is written now, or what was just reopened: it is open again.
          const status = patch.status ?? (comment.status === "applied" && patch.body !== undefined ? "open" : comment.status);
          const reopened = status !== comment.status;
          return { ...comment, ...(patch.body !== undefined ? { body: patch.body } : {}), status, ...(reopened ? { applied_in_version: undefined } : {}) };
        }),
      );
      onCommentUpdate?.(id, patch);
    },
    [change, onCommentUpdate],
  );

  const deleteComment = React.useCallback(
    (id: string) => {
      change(commentsRef.current.filter((comment) => comment.id !== id && comment.reply_to !== id));
      setSelectedId((current) => (current === id ? null : current));
      onCommentDelete?.(id);
    },
    [change, onCommentDelete],
  );

  const changeDraft = React.useCallback(
    (next: CommentDraft | null, options?: { compose?: boolean; seekTo?: number }) => {
      // Moving the ends of the range being commented on is fine. Starting a new selection is not.
      if (options?.compose && locked && next !== draft) return refuse();
      setDraft(next);
      setNotice(undefined);
      if (options?.seekTo !== undefined) {
        player.pause();
        player.seek(options.seekTo);
      }
      if (options?.compose !== undefined) setComposing(options.compose && next !== null);
      if (next && options?.compose) {
        setSelectedId(null);
        setPinMode(false);
      }
    },
    [locked, draft, refuse, player],
  );

  const placePin = (pin: Pin) => {
    player.pause();
    const at = player.playhead.get();
    setDraft({ start_ms: draft?.start_ms ?? at, end_ms: draft?.end_ms ?? at, pin });
    setComposing(true);
    setSelectedId(null);
    setPinMode(false);
  };

  // --- Keyboard. The hook skips bare keys while a text field has focus, so typing a comment never moves the player.
  useHotkey(
    " ",
    () => {
      player.toggle();
    },
    { skip: spaceBelongsToTarget },
  );
  useHotkey("k", player.toggle);
  useHotkey("j", () => player.skip(-JUMP_MS));
  useHotkey("l", () => player.skip(JUMP_MS));
  useHotkey(",", () => player.step(-1));
  useHotkey(".", () => player.step(1));
  useHotkey("p", () => setPinMode((on) => !on));

  const mark = (fn: typeof markIn) => {
    const range = draft && draft.end_ms > draft.start_ms ? draft : null;
    setDraft({ ...fn(range, player.playhead.get(), version.duration_ms), ...(draft?.pin ? { pin: draft.pin } : {}) });
    setComposing(false);
    setSelectedId(null);
  };
  useHotkey("i", () => mark(markIn));
  useHotkey("o", () => mark(markOut));
  useHotkey("c", () => {
    if (!draft) setDraft({ start_ms: player.playhead.get(), end_ms: player.playhead.get() });
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
          <VersionSelector versions={versions} latest={latest} selected={version.version} onSelect={selectVersion} className="min-w-0 flex-1" />
          {localVideo && (
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
          )}
        </div>

        {version.version !== latest && (
          <p className="rounded-[10px] bg-wash px-3 py-2 text-[12.5px] text-muted-foreground">
            You are looking at v{version.version}, not the latest version. Comments made on newer versions are not on it yet.
          </p>
        )}

        {aboveVideo?.({ version, latest, selectVersion })}

        <PreviewPlayer
          player={player}
          version={version}
          src={videoSrc}
          placeholderLabel={placeholderLabel}
          onVideoError={onVideoError}
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
          body={draftBody}
          onBodyChange={setDraftBody}
          notice={notice}
          onDraftChange={changeDraft}
          onSaveDraft={saveDraft}
          onCancelDraft={clearDraft}
          onSelectRegion={selectRegion}
          onHoverRegion={setHoveredId}
        />

        <ShortcutLegend />

        {belowTimeline?.({ version, latest, selectVersion })}
      </div>

      <CommentPanel
        items={resolved}
        selectedId={selectedId}
        hoveredId={hoveredId}
        onSelect={selectComment}
        onHover={setHoveredId}
        onOpenVersion={openVersion}
        note={commentsNote}
        error={commentsError}
        onDismissError={onDismissCommentsError}
        onResolve={onCommentUpdate ? (id, resolved) => updateComment(id, { status: resolved ? "resolved" : "open" }) : undefined}
        onEdit={onCommentUpdate ? (id, body) => updateComment(id, { body }) : undefined}
        onDelete={onCommentDelete ? deleteComment : undefined}
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
