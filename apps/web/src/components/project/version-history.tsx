import { Bot, User } from "lucide-react";
import type { TimelineVersionSummary } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { relativeTime } from "./relative-time";

/**
 * Every version of the timeline with what changed in it, newest first. Choosing one shows it in the review above. A version
 * is never changed once made, so an older one can always be looked at again.
 */
export function VersionHistory({
  versions,
  selected,
  onSelect,
  now = Date.now(),
  className,
}: {
  versions: readonly TimelineVersionSummary[];
  selected: number;
  onSelect: (version: number) => void;
  now?: number;
  className?: string;
}) {
  if (versions.length === 0) return null;
  const latest = Math.max(...versions.map((version) => version.version));
  return (
    <section aria-labelledby="version-history-title" className={cn("rounded-xl bg-surface-raised shadow-soft ring-1 ring-border", className)}>
      <h2 id="version-history-title" className="border-b border-hairline px-3 py-2.5 text-[13px] font-semibold tracking-[-0.01em]">
        Versions
      </h2>
      <ol className="divide-y divide-hairline">
        {versions.map((version) => {
          const lines = version.summary.filter((line) => line.trim() !== "");
          const isSelected = version.version === selected;
          return (
            <li key={version.version}>
              <button
                type="button"
                aria-current={isSelected ? "true" : undefined}
                onClick={() => onSelect(version.version)}
                className={cn(
                  "flex w-full cursor-pointer items-start gap-3 px-3 py-2.5 text-left outline-none transition-colors hover:bg-wash focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset",
                  isSelected && "bg-brand-wash",
                )}
              >
                <span className="mt-0.5 w-9 shrink-0 font-mono text-[12.5px] font-medium tabular-nums">v{version.version}</span>
                <span className="min-w-0 flex-1 space-y-0.5">
                  {lines.length > 0 ? (
                    <span className="block text-[13px] leading-5">
                      {lines.map((line) => (
                        <span key={line} className="block text-pretty">
                          {line}
                        </span>
                      ))}
                    </span>
                  ) : (
                    <span className="block text-[13px] text-muted-foreground">No changes listed</span>
                  )}
                  <span className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                    {version.author_kind === "agent" ? <Bot aria-hidden className="size-3.5" /> : <User aria-hidden className="size-3.5" />}
                    {version.author_kind === "agent" ? "Editor" : "You"} · {relativeTime(version.created_at, now)}
                    {version.version === latest ? <span className="rounded-full bg-wash px-1.5 text-[11px] text-subtle-foreground">latest</span> : null}
                  </span>
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
