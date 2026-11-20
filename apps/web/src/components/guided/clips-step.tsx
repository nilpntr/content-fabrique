import { Link } from "@tanstack/react-router";
import { ArrowRight, Check, CircleAlert, Film, Loader2, Music, Upload, X } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { BigButton, CARD, Screen, ScreenTitle } from "./ui";
import type { ClipState, GuidedClip, GuidedClips } from "./use-guided-clips";

const VIDEO_ACCEPT = "video/mp4,video/quicktime,video/webm,audio/*,.mp4,.mov,.webm,.m4a,.mp3,.wav,.aac";
const SONG_ACCEPT = "audio/*,.m4a,.mp3,.wav,.aac,.ogg,.flac";

const hasFiles = (event: React.DragEvent): boolean => [...event.dataTransfer.types].includes("Files");

/** Where a clip is, as an icon and words (never a colour alone). */
function StateLine({ state }: { state: ClipState }) {
  if (state.kind === "ready") {
    return (
      <span className="flex items-center gap-1.5 text-[13.5px] text-muted-foreground">
        <Check aria-hidden strokeWidth={2.5} className="size-4 text-brand" />
        Ready
      </span>
    );
  }
  if (state.kind === "problem") {
    return (
      <span className="flex items-start gap-1.5 text-[13.5px] font-medium text-pretty">
        <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
        {state.message}
      </span>
    );
  }
  return (
    <span className="flex items-center gap-1.5 text-[13.5px] text-muted-foreground">
      <Loader2 aria-hidden className="size-4 text-brand motion-safe:animate-spin" />
      {state.kind === "uploading" ? (
        <>
          Uploading <span className="tabular-nums">{state.percent}%</span>
        </>
      ) : (
        "Getting it ready"
      )}
    </span>
  );
}

function ClipRow({ clip, onRemove, onRetry }: { clip: GuidedClip; onRemove: () => void; onRetry: () => void }) {
  const Icon = clip.song ? Music : Film;
  return (
    <li data-state={clip.state.kind} className="flex items-center gap-3 rounded-xl bg-well px-3 py-2.5 shadow-well">
      <Icon aria-hidden className="size-5 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[15px] font-medium">
          {clip.song ? <span className="text-muted-foreground">Song: </span> : null}
          {clip.name}
        </p>
        <StateLine state={clip.state} />
      </div>
      {clip.state.kind === "problem" && clip.state.retry ? (
        <Button variant="secondary" size="lg" onClick={onRetry}>
          Try again
        </Button>
      ) : null}
      <Button variant="ghost" size="lg" className="size-11 px-0" aria-label={`Remove ${clip.name}`} onClick={onRemove}>
        <X aria-hidden />
      </Button>
    </li>
  );
}

/**
 * Screen 1: one big place to hand over the clips, and an optional song. The whole card opens the phone's picker; on a desk files can
 * be dropped on it. Uploading starts at once and carries on behind the questions, so "Next" does not wait for it.
 */
export function ClipsStep({ source, onNext }: { source: GuidedClips; onNext: () => void }) {
  const [over, setOver] = React.useState(false);
  // dragenter and dragleave also fire for the children, so the zone counts them.
  const depth = React.useRef(0);
  const clipsInput = React.useRef<HTMLInputElement>(null);
  const songInput = React.useRef<HTMLInputElement>(null);
  const { clips, song } = source;
  const rows = song ? [...clips, song] : clips;

  const take = (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = [...(event.target.files ?? [])];
    event.target.value = ""; // the same file can be picked again
    if (files.length > 0) source.add(files);
  };

  return (
    <Screen>
      <ScreenTitle title="Add your clips" line="Drop in everything you filmed. I'll pick the best parts." />

      <button
        type="button"
        data-dragging={over}
        disabled={!source.available}
        onClick={() => clipsInput.current?.click()}
        onDragEnter={(event) => {
          if (!hasFiles(event)) return;
          event.preventDefault();
          depth.current++;
          setOver(true);
        }}
        onDragOver={(event) => {
          if (!hasFiles(event)) return;
          event.preventDefault(); // allows the drop
          event.dataTransfer.dropEffect = "copy";
        }}
        onDragLeave={(event) => {
          if (!hasFiles(event)) return;
          depth.current = Math.max(0, depth.current - 1);
          if (depth.current === 0) setOver(false);
        }}
        onDrop={(event) => {
          if (!hasFiles(event)) return;
          event.preventDefault();
          depth.current = 0;
          setOver(false);
          const files = [...event.dataTransfer.files];
          if (files.length > 0) source.add(files);
        }}
        className={cn(
          "flex w-full cursor-pointer flex-col items-center gap-2 rounded-3xl border-2 border-dashed border-border bg-well px-6 text-center outline-none",
          "focus-visible:border-brand disabled:cursor-not-allowed disabled:opacity-50 data-[dragging=true]:border-brand data-[dragging=true]:bg-brand-wash",
          rows.length === 0 ? "py-16" : "py-8",
        )}
      >
        <span aria-hidden className="flex size-14 items-center justify-center rounded-2xl bg-surface-raised text-brand shadow-raised">
          <Upload className="size-6" />
        </span>
        <span className="text-[19px] font-semibold tracking-[-0.015em]">{over ? "Drop them here" : rows.length === 0 ? "Tap to choose clips" : "Add more clips"}</span>
        <span className="text-[14px] text-muted-foreground">or drag them here</span>
      </button>
      <input ref={clipsInput} type="file" multiple accept={VIDEO_ACCEPT} tabIndex={-1} aria-hidden="true" data-testid="clips-input" className="hidden" onChange={take} />
      <input ref={songInput} type="file" accept={SONG_ACCEPT} tabIndex={-1} aria-hidden="true" data-testid="song-input" className="hidden" onChange={take} />

      {source.notice ? (
        <p role="alert" className="flex items-start gap-2 rounded-xl bg-wash px-4 py-3 text-[15px] font-medium text-pretty">
          <CircleAlert aria-hidden className="mt-0.5 size-5 shrink-0" />
          {source.notice}
        </p>
      ) : null}

      {rows.length > 0 ? (
        <ul aria-label="Your clips" className={cn(CARD, "space-y-2 p-3")}>
          {rows.map((clip) => (
            <ClipRow key={clip.key} clip={clip} onRemove={() => source.remove(clip.key)} onRetry={() => source.retry(clip.key)} />
          ))}
        </ul>
      ) : null}

      {song ? null : (
        <Button variant="secondary" size="lg" className="h-12 w-full rounded-2xl text-[15px]" disabled={!source.available} onClick={() => songInput.current?.click()}>
          <Music aria-hidden />
          Add a song <span className="font-normal text-muted-foreground">(optional)</span>
        </Button>
      )}

      <BigButton disabled={clips.length === 0} onClick={onNext}>
        Next
        <ArrowRight aria-hidden />
      </BigButton>

      <p className="text-center text-[14px] text-muted-foreground">
        Already uploaded your clips?{" "}
        <Link to="/projects/new" className="font-medium text-brand underline-offset-4 hover:underline">
          Pick from your library
        </Link>
      </p>
    </Screen>
  );
}
