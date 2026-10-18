import { Check, Film, Pencil, Sparkles, X } from "lucide-react";
import * as React from "react";
import { formatDuration } from "@/components/new-project/duration";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ApiError } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { StatusChip } from "./clip-card";
import type { Asset } from "./types";

/** The words for a rename the API refused or could not make. */
export function renameMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "Your role in this workspace can't rename clips. Ask an owner or editor.";
    if (error.status === 400) return "That name was not accepted. Use 1 to 255 characters.";
    if (error.status === 404) return "This clip is gone, so it could not be renamed.";
    if (error.status === 0) return "The server could not be reached. Try again.";
  }
  return "The clip could not be renamed. Try again.";
}

const date = (iso: string): string => {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? "" : parsed.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
};

/** What the quick look has to say about a clip that has no description yet. */
function quickLookNote(asset: Asset): string | undefined {
  if (asset.description?.trim()) return undefined;
  if (asset.status !== "ready") return "I'll look at this clip as soon as it's ready.";
  if (asset.quickLook === "failed") return "I couldn't describe this clip. You can still rename it.";
  // Absent for clips the quick look does not apply to (older uploads), and skipped when nothing is owed.
  if (asset.quickLook !== "pending") return "No description for this clip.";
  return "I'm watching this clip. What's in it shows up here in a moment.";
}

function TitleEditor({ title, onRename }: { title: string; onRename: (title: string) => Promise<void> }) {
  const [editing, setEditing] = React.useState(false);
  const [value, setValue] = React.useState(title);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState<string | undefined>();
  const id = React.useId();

  async function save(event: React.FormEvent) {
    event.preventDefault();
    const next = value.trim();
    if (next === "" || saving) return;
    if (next === title) {
      setEditing(false);
      return;
    }
    setSaving(true);
    setError(undefined);
    try {
      await onRename(next);
      setEditing(false);
    } catch (failure) {
      setError(renameMessage(failure));
    } finally {
      setSaving(false);
    }
  }

  if (!editing) {
    return (
      <div className="flex min-w-0 items-start gap-1.5">
        <SheetTitle className="min-w-0 flex-1 text-[17px] leading-snug tracking-[-0.015em] text-balance break-words">{title}</SheetTitle>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Rename the clip"
          onClick={() => {
            setValue(title);
            setError(undefined);
            setEditing(true);
          }}
        >
          <Pencil />
        </Button>
      </div>
    );
  }
  return (
    <form onSubmit={save} className="space-y-1.5">
      <SheetTitle className="sr-only">Rename the clip</SheetTitle>
      <label htmlFor={id} className="sr-only">
        Clip name
      </label>
      <div className="flex items-center gap-1.5">
        <Input id={id} autoFocus value={value} maxLength={255} disabled={saving} onChange={(event) => setValue(event.target.value)} onKeyDown={(event) => {
            // Escape leaves the field, not the whole sheet.
            if (event.key !== "Escape") return;
            event.stopPropagation();
            setEditing(false);
          }}
        />
        <Button type="submit" size="icon" aria-label="Save the name" disabled={saving || value.trim() === ""}>
          <Check />
        </Button>
        <Button type="button" variant="ghost" size="icon" aria-label="Cancel renaming" disabled={saving} onClick={() => setEditing(false)}>
          <X />
        </Button>
      </div>
      {error ? (
        <p role="alert" className="text-[12.5px] text-destructive">
          {error}
        </p>
      ) : null}
    </form>
  );
}

/**
 * A clip in the library, opened: the loop, the name (which can be changed), what the quick look wrote (a one-line description and
 * the tags) and the facts of the file. Renaming keeps the name for good: the quick look never overwrites a name the creator wrote.
 */
export function ClipDetails({ asset, onClose, onRename }: { asset: Asset | null; onClose: () => void; onRename: (asset: Asset, title: string) => Promise<void> }) {
  const note = asset ? quickLookNote(asset) : undefined;
  return (
    <Sheet open={asset !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md" showCloseButton>
        {asset ? (
          <>
            <div className="relative aspect-video w-full shrink-0 overflow-hidden bg-[oklch(0.17_0.006_80)]">
              {asset.hoverPreviewUrl ? (
                <video src={asset.hoverPreviewUrl} poster={asset.posterUrl} muted loop autoPlay playsInline aria-label="Preview loop of the clip" className="size-full object-contain" />
              ) : asset.posterUrl ? (
                <img src={asset.posterUrl} alt="" className="size-full object-contain" />
              ) : (
                <div className="flex size-full items-center justify-center text-white/40">
                  <Film aria-hidden className="size-8" />
                </div>
              )}
            </div>
            <SheetHeader className="gap-2 pt-0">
              <TitleEditor key={`${asset.id}:${asset.title}`} title={asset.title} onRename={(title) => onRename(asset, title)} />
              <SheetDescription className="sr-only">Details of this clip</SheetDescription>
              <div className="flex flex-wrap items-center gap-2">
                <StatusChip asset={asset} />
                {asset.durationMs > 0 ? <span className="font-mono text-[12px] text-muted-foreground tabular-nums">{formatDuration(asset.durationMs)}</span> : null}
                <span className="text-[12px] text-subtle-foreground tabular-nums">
                  {asset.width}×{asset.height}
                </span>
                {date(asset.createdAt) ? <span className="text-[12px] text-subtle-foreground">Added {date(asset.createdAt)}</span> : null}
              </div>
            </SheetHeader>
            <section aria-label="What's in it" className="space-y-2.5 px-4 pb-6">
              <h3 className="flex items-center gap-1.5 text-[11px] font-medium tracking-[0.08em] text-subtle-foreground uppercase">
                <Sparkles aria-hidden className="size-3.5" />
                What's in it
              </h3>
              {asset.description?.trim() ? <p className="text-[13.5px] leading-relaxed text-pretty">{asset.description}</p> : null}
              {note ? <p className="text-[13px] text-muted-foreground">{note}</p> : null}
              {asset.tags.length > 0 ? (
                <ul aria-label="Tags" className="flex flex-wrap gap-1.5">
                  {asset.tags.map((tag) => (
                    <li key={tag} className={cn("rounded-full bg-wash px-2.5 py-0.5 text-[12px] text-muted-foreground")}>
                      {tag}
                    </li>
                  ))}
                </ul>
              ) : null}
            </section>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
