import { Clapperboard, Film, Lightbulb } from "lucide-react";
import * as React from "react";
import { cn } from "@/lib/utils";
import type { StoryIdea, Storyboard } from "./storyboard";

/** A frame for a moment: the poster of its clip, or a quiet stand-in. */
function Frame({ src }: { src?: string }) {
  const [broken, setBroken] = React.useState(false);
  return (
    <span aria-hidden className="relative block aspect-[9/16] w-12 shrink-0 overflow-hidden rounded-lg bg-well shadow-well sm:w-14">
      {src && !broken ? (
        <img src={src} alt="" loading="lazy" draggable={false} onError={() => setBroken(true)} className="size-full object-cover" />
      ) : (
        <span className="grid size-full place-items-center">
          <Film className="size-4 text-subtle-foreground" />
        </span>
      )}
    </span>
  );
}

/** The idea of the video in plain words: "The idea: Transformation reveal", why it fits, and whether it is new for the creator. */
function IdeaLine({ idea }: { idea: StoryIdea }) {
  return (
    <div data-testid="storyboard-idea" className="space-y-1">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[14px]">
        <Lightbulb aria-hidden className="size-4 shrink-0 text-brand" />
        <span className="text-muted-foreground">The idea:</span>
        <span className="font-semibold">{idea.name}</span>
        {idea.mix ? <span className="text-muted-foreground">{idea.mix}</span> : null}
        {idea.isNew ? <span className="rounded-full bg-add-wash px-2 py-0.5 text-[11.5px] font-medium text-good">New for you</span> : null}
      </p>
      {idea.why ? <p className="max-w-prose pl-6 text-[13px] text-pretty text-muted-foreground">{idea.why}</p> : null}
    </div>
  );
}

/** The plan as a storyboard: how the video opens, what happens, how it ends, each with a frame from its clip. */
export function StoryboardView({ board, posters, title = "The plan", className }: { board: Storyboard; posters: ReadonlyMap<string, string>; title?: string; className?: string }) {
  const titleId = React.useId();
  return (
    <section aria-labelledby={titleId} data-testid="storyboard" className={cn("space-y-3 rounded-2xl bg-surface-raised p-4 shadow-soft ring-1 ring-border sm:p-5", className)}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h2 id={titleId} className="flex items-center gap-2 text-[15px] font-semibold tracking-[-0.01em]">
          <Clapperboard aria-hidden className="size-4 text-brand" />
          {title}
        </h2>
        {board.length ? <p className="text-[13px] text-muted-foreground">{board.length}</p> : null}
      </div>
      {board.idea ? <IdeaLine idea={board.idea} /> : null}
      {board.intro ? <p className="max-w-prose text-[14px] text-pretty text-muted-foreground">{board.intro}</p> : null}
      <ol className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
        {board.cards.map((card, index) => (
          <React.Fragment key={card.key}>
            {board.more > 0 && index === board.cards.length - 1 ? (
              <li className="flex items-center rounded-xl px-3 py-2 text-[13px] text-subtle-foreground">…and {board.more} more {board.more === 1 ? "moment" : "moments"}</li>
            ) : null}
            <li data-place={card.place} className="flex items-center gap-3 rounded-xl bg-well p-2 pr-3 shadow-well">
              <Frame src={card.assetId ? posters.get(card.assetId) : undefined} />
              <p className="min-w-0 text-[13.5px] leading-snug text-pretty">{card.line}</p>
            </li>
          </React.Fragment>
        ))}
      </ol>
    </section>
  );
}
