import { Autocomplete } from "@base-ui/react/autocomplete";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { useNavigate } from "@tanstack/react-router";
import { CornerDownLeft, FilmIcon, FolderOpenIcon, type LucideIcon, MoonIcon, SearchIcon, SunIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { HighlightGlider } from "@/components/ui/highlight-glider";
import { Kbd } from "@/components/ui/kbd";
import { toggleTheme, useTheme } from "@/hooks/use-theme";
import { useAssets } from "@/lib/api/hooks";
import { useProjects } from "@/lib/api/project-hooks";
import { searchClips, searchProjects } from "@/lib/search";
import { projectStatusWords } from "@/lib/words";
import { NAV_ITEMS } from "@/nav";

interface PaletteItem {
  id: string;
  label: string;
  icon: LucideIcon;
  /** A line after the label: the status of a project, the tags of a clip. */
  hint?: string;
  /** A small picture instead of the icon (a clip's poster). */
  thumb?: string;
  run: () => void;
}

interface PaletteGroup {
  value: string;
  items: PaletteItem[];
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [query, setQuery] = useState("");
  const navigate = useNavigate();
  const theme = useTheme();

  // Closing leaves the query alone (clearing it would re-render the full list mid-fade; see onOpenChangeComplete).
  // Opening clears it, which covers a reopen during the exit animation, when that never completes.
  useEffect(() => {
    if (open) setQuery("");
  }, [open]);

  const close = () => onOpenChange(false);
  const pages: PaletteItem[] = NAV_ITEMS.map((item) => ({
    id: `go:${item.to}`,
    label: `Go to ${item.title}`,
    icon: item.icon,
    run: () => {
      close();
      void navigate({ to: item.to });
    },
  }));
  const actions: PaletteItem[] = [
    {
      id: "theme",
      label: theme === "dark" ? "Switch to light mode" : "Switch to dark mode",
      icon: theme === "dark" ? SunIcon : MoonIcon,
      run: toggleTheme,
    },
  ];

  // Projects and clips are looked up in what the app already holds (the first pages of each), and only once the palette has been opened.
  const [asked, setAsked] = useState(false);
  useEffect(() => {
    if (open) setAsked(true);
  }, [open]);
  const projects = useProjects({ enabled: asked });
  // The palette only reads the list: it does not poll for clips that are still processing (the library does that), whatever it holds.
  const assets = useAssets({ enabled: asked, isOwned: () => true });
  // While the palette is open, the older pages come in one after another; after a failure it stops and the label below stays.
  const { hasNextPage: moreProjects, isFetchingNextPage: loadingProjects, isFetchNextPageError: projectsFailed, fetchNextPage: nextProjects } = projects;
  const { hasNextPage: moreClips, isFetchingNextPage: loadingClips, isFetchNextPageError: clipsFailed, fetchNextPage: nextClips } = assets;
  useEffect(() => {
    if (open && moreProjects && !loadingProjects && !projectsFailed) void nextProjects();
  }, [open, moreProjects, loadingProjects, projectsFailed, nextProjects]);
  useEffect(() => {
    if (open && moreClips && !loadingClips && !clipsFailed) void nextClips();
  }, [open, moreClips, loadingClips, clipsFailed, nextClips]);
  /** Some projects or clips are not searched (yet): the first pages are, the rest are on their way or could not be loaded. */
  const partial = moreProjects || moreClips;
  const projectItems: PaletteItem[] = searchProjects(projects.data ?? [], query).map((project) => ({
    id: `project:${project.id}`,
    label: project.title,
    icon: FolderOpenIcon,
    hint: projectStatusWords(project).label,
    run: () => {
      close();
      void navigate({ to: "/projects/$projectId", params: { projectId: project.id } });
    },
  }));
  const clipItems: PaletteItem[] = searchClips(assets.data ?? [], query).map((asset) => ({
    id: `clip:${asset.id}`,
    label: asset.title || asset.filename,
    icon: FilmIcon,
    hint: asset.tags.length > 0 ? asset.tags.slice(0, 2).join(" · ") : "Library",
    thumb: asset.derivatives?.poster?.url,
    run: () => {
      close();
      // The library opens that clip's sheet.
      void navigate({ to: "/library", search: { state: undefined, samples: undefined, open: asset.id } });
    },
  }));

  const needle = query.trim().toLowerCase();
  const groups: PaletteGroup[] = [
    { value: "Projects", items: projectItems },
    { value: "Clips", items: clipItems },
    ...[
      { value: "Pages", items: pages },
      { value: "Actions", items: actions },
    ].map((group) => ({ ...group, items: group.items.filter((item) => !needle || item.label.toLowerCase().includes(needle)) })),
  ].filter((group) => group.items.length > 0);

  return (
    // The query is cleared once the exit animation has finished: clearing it on close would re-render the full list mid-fade.
    <DialogPrimitive.Root
      open={open}
      onOpenChange={onOpenChange}
      onOpenChangeComplete={(isOpen) => {
        if (!isOpen) setQuery("");
      }}
    >
      <DialogPrimitive.Portal>
        <DialogPrimitive.Backdrop className="fixed inset-0 z-50 bg-[oklch(0.25_0.01_80/0.18)] backdrop-blur-[2px] transition-opacity duration-(--dur-base) data-ending-style:opacity-0 data-starting-style:opacity-0 dark:bg-black/55" />
        <DialogPrimitive.Popup
          aria-label="Search"
          className="fixed top-[13vh] left-1/2 z-50 flex max-h-[min(34rem,74vh)] w-[calc(100%-2rem)] max-w-[38rem] -translate-x-1/2 flex-col overflow-hidden rounded-[18px] bg-popover shadow-pop outline-none transition-[opacity,scale,translate] duration-(--dur-slow) ease-out data-ending-style:-translate-y-[calc(0.25rem)] data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-ending-style:duration-(--dur-fast) data-starting-style:-translate-y-[calc(0.5rem)] data-starting-style:scale-[0.97] data-starting-style:opacity-0"
        >
          <Autocomplete.Root
            inline
            open
            mode="none"
            items={groups}
            value={query}
            onValueChange={(value, details) => {
              if (details.reason === "input-change" || details.reason === "input-clear") setQuery(value);
            }}
            autoHighlight="always"
            keepHighlight
            itemToStringValue={(item: PaletteItem) => item.label}
          >
            <div className="flex h-14 shrink-0 items-center gap-3 border-b border-hairline px-4 dark:border-border">
              <SearchIcon className="size-4.5 shrink-0 text-subtle-foreground" />
              <Autocomplete.Input
                placeholder="Search your videos, clips and pages…"
                onKeyDown={(event) => {
                  if (event.key === "Escape") close();
                }}
                className="h-full min-w-0 flex-1 bg-transparent text-[15px] tracking-[-0.01em] outline-none placeholder:text-subtle-foreground focus-visible:ring-0 focus-visible:ring-offset-0"
              />
              <Kbd>esc</Kbd>
            </div>

            <Autocomplete.List className="relative isolate min-h-0 flex-1 scroll-py-2 overflow-y-auto overscroll-contain p-2 data-empty:hidden">
              <HighlightGlider className="rounded-[11px] bg-accent" />
              {groups.map((group) => (
                <Autocomplete.Group key={group.value} items={group.items} className="pb-1.5 last:pb-0">
                  <Autocomplete.GroupLabel className="px-3 pt-2.5 pb-1.5 text-[10.5px] font-medium tracking-[0.08em] text-subtle-foreground uppercase">
                    {group.value}
                  </Autocomplete.GroupLabel>
                  <Autocomplete.Collection>
                    {(item: PaletteItem) => (
                      <Autocomplete.Item
                        key={item.id}
                        value={item}
                        onClick={item.run}
                        className="group flex h-11 cursor-pointer items-center gap-3 rounded-[11px] px-3 text-[13.5px] text-muted-foreground outline-none select-none transition-colors data-highlighted:text-foreground"
                      >
                        {item.thumb ? (
                          <img src={item.thumb} alt="" loading="lazy" draggable={false} className="size-6 shrink-0 rounded-[7px] bg-wash object-cover shadow-raised" />
                        ) : (
                          <span className="flex size-6 shrink-0 items-center justify-center rounded-[7px] bg-surface shadow-raised transition-colors group-data-[highlighted]:text-brand">
                            <item.icon className="size-3.5" />
                          </span>
                        )}
                        <span className="min-w-0 flex-1 truncate">{item.label}</span>
                        {item.hint ? <span className="hidden max-w-[10rem] shrink-0 truncate text-[12px] text-subtle-foreground sm:inline">{item.hint}</span> : null}
                        <CornerDownLeft className="size-3.5 shrink-0 -translate-x-1 text-subtle-foreground opacity-0 transition-[opacity,translate] duration-(--dur-base) group-data-[highlighted]:translate-x-0 group-data-[highlighted]:opacity-100" />
                      </Autocomplete.Item>
                    )}
                  </Autocomplete.Collection>
                </Autocomplete.Group>
              ))}
            </Autocomplete.List>

            <Autocomplete.Empty className="px-4 py-10 text-center text-[13.5px] text-muted-foreground empty:hidden">
              No results for “{query.trim()}”{partial ? " in your recent projects and clips" : ""}.
            </Autocomplete.Empty>
          </Autocomplete.Root>

          <div className="flex h-10 shrink-0 items-center gap-4 border-t border-hairline bg-surface/60 px-4 text-[12px] text-subtle-foreground dark:border-border dark:bg-transparent">
            <span className="inline-flex items-center gap-1.5">
              <Kbd>↑</Kbd>
              <Kbd>↓</Kbd> move
            </span>
            <span className="inline-flex items-center gap-1.5">
              <Kbd>↵</Kbd> select
            </span>
            {partial ? (
              <span data-testid="palette-scope" className="ml-auto truncate">
                Searching your recent projects and clips
              </span>
            ) : null}
          </div>
        </DialogPrimitive.Popup>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
