import { Bot, User } from "lucide-react";
import { cn } from "@/lib/utils";
import { versionName } from "@/lib/words";
import type { ReviewVersion } from "./types";

export interface VersionSelectorProps {
  versions: ReviewVersion[];
  /** The newest version number, computed once by the screen. */
  latest: number;
  selected: number;
  onSelect: (version: number) => void;
  /** The line under the switch: what changed in the version on screen. Defaults to the version's own summary. */
  describe?: (version: ReviewVersion) => string;
  className?: string;
}

/** Version 1, Version 2, … above the player: a switch to compare them. Comments follow the choice: they are mapped onto the version on screen. */
export function VersionSelector({ versions, latest, selected, onSelect, describe, className }: VersionSelectorProps) {
  const current = versions.find((version) => version.version === selected);

  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <fieldset className="-mx-1 flex max-w-full overflow-x-auto px-1 [scrollbar-width:none]">
        <legend className="sr-only">Version</legend>
        <div className="flex rounded-[10px] bg-wash p-0.5">
          {versions.map((version) => (
            <button
              key={version.version}
              type="button"
              aria-pressed={version.version === selected}
              aria-label={versionName(version.version)}
              title={version.summary}
              onClick={() => onSelect(version.version)}
              className={cn(
                "h-8 shrink-0 cursor-pointer rounded-[8px] px-3 text-[13px] font-medium whitespace-nowrap text-muted-foreground transition-colors outline-none focus-visible:ring-2 focus-visible:ring-brand",
                version.version === selected && "bg-surface-raised text-foreground shadow-raised",
              )}
            >
              {versionName(version.version)}
              {version.version === latest && <span className="ml-1.5 text-[11px] font-normal text-subtle-foreground">Latest</span>}
            </button>
          ))}
        </div>
      </fieldset>
      {current && (
        <p className="flex min-w-0 items-center gap-1.5 text-[12.5px] text-muted-foreground">
          {current.author_kind === "agent" ? <Bot className="size-3.5 shrink-0" aria-hidden /> : <User className="size-3.5 shrink-0" aria-hidden />}
          <span className="truncate">{describe ? describe(current) : current.summary}</span>
        </p>
      )}
    </div>
  );
}
