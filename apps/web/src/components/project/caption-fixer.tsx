import { Captions } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/api/client";
import { useApplyTimelineOps, useAssetTranscript, useTimelineSpec } from "@/lib/api/project-hooks";
import { cn } from "@/lib/utils";
import { atLimit, captionsOf, cleanCorrection, clipAssetIds, correctionsOf, MAX_CORRECTIONS, sameCorrections, setCaptionsOp, withCorrection } from "./caption-corrections";

/** What the editor shows of a transcript word. */
export interface CaptionWord {
  text: string;
}

/**
 * The words of one clip's transcript as a row of buttons. A click opens the word in a box; "Save" keeps the fix, "Back to original" takes it
 * away. The words are the transcript's, so the timing does not change: only what the caption says.
 */
export function CaptionWordEditor({
  words,
  fixes,
  onFix,
  busy = false,
  className,
}: {
  words: readonly CaptionWord[];
  /** The fixes by word position. */
  fixes: ReadonlyMap<number, string>;
  /** `text` is the clean replacement, or null to go back to the word the transcript heard. */
  onFix: (word: number, text: string | null) => void;
  busy?: boolean;
  className?: string;
}) {
  const [open, setOpen] = React.useState<number | undefined>(undefined);
  const [draft, setDraft] = React.useState("");
  const [problem, setProblem] = React.useState<string | undefined>(undefined);

  const start = (index: number) => {
    setOpen(index);
    setDraft(fixes.get(index) ?? words[index]?.text ?? "");
    setProblem(undefined);
  };
  const save = () => {
    if (open === undefined) return;
    const cleaned = cleanCorrection(draft);
    if (!cleaned.ok) {
      setProblem(cleaned.reason);
      return;
    }
    onFix(open, cleaned.text);
    setOpen(undefined);
  };

  return (
    <div className={cn("space-y-2", className)}>
      <p className="flex flex-wrap gap-x-1 gap-y-1 text-[14px] leading-[1.6]" data-testid="caption-words">
        {words.map((word, index) => {
          const fixed = fixes.get(index);
          return (
            <button
              // biome-ignore lint/suspicious/noArrayIndexKey: a word is its position in the transcript; the list never reorders.
              key={index}
              type="button"
              disabled={busy}
              onClick={() => start(index)}
              aria-pressed={open === index}
              aria-label={fixed === undefined ? `Fix the word ${word.text}` : `Fix the word ${fixed} (heard as ${word.text})`}
              className={cn(
                "rounded-md px-1 transition-colors hover:bg-well focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50",
                fixed !== undefined && "bg-brand-wash font-medium",
                open === index && "bg-well shadow-well",
              )}
            >
              {fixed ?? word.text}
            </button>
          );
        })}
      </p>
      {open !== undefined ? (
        <form
          className="space-y-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            save();
          }}
        >
          <label className="block text-[12.5px] text-muted-foreground" htmlFor="caption-fix-input">
            How it should read
          </label>
          <div className="flex gap-2">
            <Input
              id="caption-fix-input"
              value={draft}
              maxLength={80}
              autoFocus
              onChange={(event) => {
                setDraft(event.target.value);
                setProblem(undefined);
              }}
              aria-invalid={problem !== undefined}
            />
            <Button type="submit" size="sm" disabled={busy}>
              Save
            </Button>
            {fixes.has(open) ? (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => {
                  onFix(open, null);
                  setOpen(undefined);
                }}
              >
                Back to original
              </Button>
            ) : null}
          </div>
          {problem ? (
            <p role="alert" className="text-[12.5px] text-destructive">
              {problem}
            </p>
          ) : null}
        </form>
      ) : null}
    </div>
  );
}

/** What the creator is told when the fix could not be saved. */
export function captionFixMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "version_conflict") return "The video changed while you were editing. Reload the page and fix the word again.";
    if (error.status === 422) return "That word could not be saved. Take out symbols like < > { } | $ and keep it to 40 characters.";
  }
  return "That word could not be saved. Try again.";
}

/**
 * "Fix a caption": the words of the video's captions, so a misheard one can be corrected. A fix is saved as an edit of the timeline (no model,
 * no credits): it makes the next version, whose preview shows the corrected word. Nothing is shown for a video without captions.
 */
export function CaptionFixer({ projectId, version, latest, className }: { projectId: string; version: number; /** The newest version: a fix is an edit of it, so an older version cannot be fixed. */ latest: number; className?: string }) {
  const spec = useTimelineSpec(projectId, version);
  const captions = captionsOf(spec.data?.spec);
  const assets = React.useMemo(() => clipAssetIds(spec.data?.spec), [spec.data]);
  const [chosen, setChosen] = React.useState<string | undefined>(undefined);
  const assetId = chosen && assets.includes(chosen) ? chosen : assets[0];
  const transcript = useAssetTranscript(captions ? assetId : undefined);
  const apply = useApplyTimelineOps();
  const [error, setError] = React.useState<string | undefined>(undefined);

  if (!captions || !assetId) return null;
  if (version !== latest) {
    return (
      <p data-testid="caption-fixer-old" className={cn("text-[12.5px] text-subtle-foreground", className)}>
        Open the latest version to fix a caption.
      </p>
    );
  }
  const words = transcript.data?.words ?? [];
  const save = (word: number, text: string | null) => {
    setError(undefined);
    const next = withCorrection(captions.corrections, assetId, word, text, words[word]?.text ?? "");
    if (sameCorrections(next, captions.corrections)) {
      // Nothing to save: the word is back to what was heard already, or the list is full.
      if (text !== null && atLimit(captions.corrections, assetId, word) && text !== (words[word]?.text ?? "").trim()) {
        setError(`A video holds up to ${MAX_CORRECTIONS} fixes. Put a word back to what was heard to make room.`);
      }
      return;
    }
    apply.mutate({ projectId, baseVersion: version, ops: [setCaptionsOp(next)] }, { onError: (e) => setError(captionFixMessage(e)) });
  };

  return (
    <section aria-label="Fix a caption" data-testid="caption-fixer" className={cn("space-y-2.5", className)}>
      <h2 className="flex items-center gap-2 text-[15px] font-semibold tracking-[-0.01em]">
        <Captions aria-hidden className="size-4 text-brand" />
        Fix a caption
      </h2>
      {assets.length > 1 ? (
        <fieldset className="flex flex-wrap gap-1.5 border-0 p-0">
          <legend className="sr-only">Clip</legend>
          {assets.map((id, index) => (
            <Button key={id} type="button" size="sm" variant={id === assetId ? "default" : "ghost"} onClick={() => setChosen(id)}>
              Clip {index + 1}
            </Button>
          ))}
        </fieldset>
      ) : null}
      {transcript.isPending ? (
        <p className="text-[13px] text-muted-foreground">Loading the words…</p>
      ) : words.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">There is no speech in this clip.</p>
      ) : (
        <CaptionWordEditor
          words={words}
          fixes={correctionsOf(captions.corrections, assetId)}
          busy={apply.isPending}
          onFix={save}
        />
      )}
      {error ? (
        <p role="alert" className="text-[13px] text-destructive text-pretty">
          {error}
        </p>
      ) : (
        <p className="text-[12.5px] text-subtle-foreground">Tap a word to correct it. It makes a new version; the timing stays.</p>
      )}
    </section>
  );
}
