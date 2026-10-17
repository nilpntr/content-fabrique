import { Coins, Cpu, Hourglass, MousePointerClick, Sparkles } from "lucide-react";
import * as React from "react";
import type { Run } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { Pill } from "./badges";
import { formatElapsed, formatUsd } from "./run-steps";

type Block = { kind: "p"; text: string } | { kind: "list"; ordered: boolean; items: string[] };

const BULLET = /^\s*(?:[-*•–]|\d+[.)])\s+/;

/**
 * The editor's closing message as paragraphs and lists. The editor writes plain sentences, sometimes a few lines that start with a
 * dash or a number; those become a list. Nothing is rendered as markup: it is text a model wrote (React escapes it).
 */
export function summaryBlocks(text: string): Block[] {
  const blocks: Block[] = [];
  let paragraph: string[] = [];
  const flush = () => {
    if (paragraph.length > 0) blocks.push({ kind: "p", text: paragraph.join(" ") });
    paragraph = [];
  };
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (trimmed === "") {
      flush();
    } else if (BULLET.test(line)) {
      flush();
      const ordered = /^\s*\d/.test(line);
      const item = trimmed.replace(BULLET, "");
      const last = blocks.at(-1);
      if (last?.kind === "list" && last.ordered === ordered) last.items.push(item);
      else blocks.push({ kind: "list", ordered, items: [item] });
    } else {
      paragraph.push(trimmed);
    }
  }
  flush();
  return blocks;
}

/** The finish: the editor's note to the creator, and the version it ended on. */
export function FinishSummary({ text, version, className }: { text: string; version?: number | null; className?: string }) {
  const blocks = React.useMemo(() => summaryBlocks(text), [text]);
  return (
    <figure className={cn("space-y-2 rounded-xl border-l-2 border-brand/60 bg-brand-wash/60 py-2.5 pr-3.5 pl-3.5", className)}>
      <figcaption className="flex items-center gap-2 text-[11px] font-medium tracking-[0.08em] text-brand uppercase">
        <Sparkles aria-hidden className="size-3.5" />
        From the editor
        {version ? (
          <Pill tone="brand" className="tracking-normal normal-case">
            Cut v{version}
          </Pill>
        ) : null}
      </figcaption>
      <div data-testid="run-summary" className="max-w-prose space-y-1.5 text-[13.5px] leading-[1.55] text-pretty">
        {blocks.map((block, index) =>
          block.kind === "p" ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: the blocks are made from the text and never reorder.
            <p key={index}>{block.text}</p>
          ) : block.ordered ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: as above.
            <ol key={index} className="list-decimal space-y-0.5 pl-5 marker:text-subtle-foreground">
              {block.items.map((item, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: as above.
                <li key={i}>{item}</li>
              ))}
            </ol>
          ) : (
            // biome-ignore lint/suspicious/noArrayIndexKey: as above.
            <ul key={index} className="list-disc space-y-0.5 pl-5 marker:text-subtle-foreground">
              {block.items.map((item, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: as above.
                <li key={i}>{item}</li>
              ))}
            </ul>
          ),
        )}
      </div>
    </figure>
  );
}

/**
 * What the run cost the creator in credits. Credits arrive with the spend-control work (docs/design/spend-control-v1.md, PR #138):
 * `Run` has no field for them yet, so {@link runCreditsOf} returns nothing and the line shows only the model, the time and the calls.
 */
export type RunCredits = { state: "charged"; credits: number } | { state: "refunded" };

/**
 * TODO(#138): read the credits the run was charged (or that were released) from the run once the API says so. `GET /v1/runs/{id}`
 * has no such field on main and none is invented here: until it has, the slot stays empty.
 */
export function runCreditsOf(_run: Run): RunCredits | undefined {
  return undefined;
}

function creditsText(credits: RunCredits): string {
  if (credits.state === "refunded") return "Credits refunded";
  return `${credits.credits} ${credits.credits === 1 ? "credit" : "credits"}`;
}

const titleCase = (word: string): string => word.replace(/^./, (c) => c.toUpperCase());

/** "claude-opus-5-5" as "Opus 5.5", "claude-opus-4-20250514" as "Opus 4", "claude-3-5-sonnet-20241022" as "Sonnet 3.5", "claude-haiku" as "Haiku". Any other model id is shown as it is. */
export function modelName(model: string): string {
  // A date suffix (eight digits) is never a version.
  const bare = model.replace(/-\d{8}$/, "");
  const named = /^claude-([a-z]+)(?:-(\d{1,2})(?:-(\d{1,2}))?)?$/.exec(bare);
  if (named?.[1]) return named[2] ? `${titleCase(named[1])} ${named[2]}${named[3] ? `.${named[3]}` : ""}` : titleCase(named[1]);
  const older = /^claude-(\d{1,2})(?:-(\d{1,2}))?-([a-z]+)$/.exec(bare);
  if (older?.[3]) return `${titleCase(older[3])} ${older[1]}${older[2] ? `.${older[2]}` : ""}`;
  return model;
}

/** The cost line of a run: the model, how long it worked, how many calls it made and what it cost; the credits when the API says. */
export function RunCostLine({ run, credits, className }: { run: Pick<Run, "model" | "active_ms" | "tool_calls" | "cost_micros" | "budget_micros">; credits?: RunCredits; className?: string }) {
  return (
    <ul aria-label="What this run used" data-testid="run-cost" className={cn("flex flex-wrap items-center gap-x-3.5 gap-y-1 font-mono text-[12px] text-muted-foreground tabular-nums", className)}>
      <li className="inline-flex items-center gap-1.5" title={run.model}>
        <Cpu aria-hidden className="size-3.5 text-subtle-foreground" />
        {modelName(run.model)}
      </li>
      <li className="inline-flex items-center gap-1.5">
        <Hourglass aria-hidden className="size-3.5 text-subtle-foreground" />
        {formatElapsed(run.active_ms)}
      </li>
      <li className="inline-flex items-center gap-1.5">
        <MousePointerClick aria-hidden className="size-3.5 text-subtle-foreground" />
        {run.tool_calls} {run.tool_calls === 1 ? "tool call" : "tool calls"}
      </li>
      <li className="inline-flex items-baseline gap-1">
        <span className="text-foreground">{formatUsd(run.cost_micros)}</span>
        <span className="text-subtle-foreground">of {formatUsd(run.budget_micros)}</span>
      </li>
      {credits ? (
        <li className="inline-flex items-center gap-1.5 font-sans font-medium text-foreground" data-slot="credits">
          <Coins aria-hidden className="size-3.5 text-subtle-foreground" />
          {creditsText(credits)}
        </li>
      ) : null}
    </ul>
  );
}
