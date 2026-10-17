import { Bot, User } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ReviewVersion } from "./types";

export interface VersionSelectorProps {
  versions: ReviewVersion[];
  /** The newest version number, computed once by the screen. */
  latest: number;
  selected: number;
  onSelect: (version: number) => void;
  className?: string;
}

/** v1, v2, … above the player. Comments follow the choice: they are mapped onto the version on screen. */
export function VersionSelector({ versions, latest, selected, onSelect, className }: VersionSelectorProps) {
  const current = versions.find((version) => version.version === selected);

  return (
    <div className={cn("flex flex-wrap items-center gap-x-3 gap-y-1.5", className)}>
      <fieldset className="flex rounded-[10px] bg-wash p-0.5">
        <legend className="sr-only">Version</legend>
        {versions.map((version) => (
          <button
            key={version.version}
            type="button"
            aria-pressed={version.version === selected}
            title={version.summary}
            onClick={() => onSelect(version.version)}
            className={cn(
              "h-7 cursor-pointer rounded-[8px] px-3 text-[13px] font-medium text-muted-foreground transition-colors outline-none focus-visible:ring-2 focus-visible:ring-brand",
              version.version === selected && "bg-surface-raised text-foreground shadow-raised",
            )}
          >
            v{version.version}
            {version.version === latest && <span className="ml-1.5 text-[11px] font-normal text-subtle-foreground">latest</span>}
          </button>
        ))}
      </fieldset>
      {current && (
        <p className="flex min-w-0 items-center gap-1.5 text-[12.5px] text-muted-foreground">
          {current.author_kind === "agent" ? <Bot className="size-3.5 shrink-0" aria-hidden /> : <User className="size-3.5 shrink-0" aria-hidden />}
          <span className="truncate">{current.summary}</span>
        </p>
      )}
    </div>
  );
}
