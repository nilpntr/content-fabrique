import { Autocomplete } from "@base-ui/react/autocomplete";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { useNavigate } from "@tanstack/react-router";
import { CornerDownLeft, type LucideIcon, MoonIcon, SearchIcon, SunIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { HighlightGlider } from "@/components/ui/highlight-glider";
import { Kbd } from "@/components/ui/kbd";
import { toggleTheme, useTheme } from "@/hooks/use-theme";
import { NAV_ITEMS } from "@/nav";

interface PaletteItem {
  id: string;
  label: string;
  icon: LucideIcon;
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

  const needle = query.trim().toLowerCase();
  const groups: PaletteGroup[] = [
    { value: "Pages", items: pages },
    { value: "Actions", items: actions },
  ]
    .map((group) => ({ ...group, items: group.items.filter((item) => !needle || item.label.toLowerCase().includes(needle)) }))
    .filter((group) => group.items.length > 0);

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
                placeholder="Search pages and actions…"
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
                        <span className="flex size-6 shrink-0 items-center justify-center rounded-[7px] bg-surface shadow-raised transition-colors group-data-[highlighted]:text-brand">
                          <item.icon className="size-3.5" />
                        </span>
                        <span className="min-w-0 flex-1 truncate">{item.label}</span>
                        <CornerDownLeft className="size-3.5 shrink-0 -translate-x-1 text-subtle-foreground opacity-0 transition-[opacity,translate] duration-(--dur-base) group-data-[highlighted]:translate-x-0 group-data-[highlighted]:opacity-100" />
                      </Autocomplete.Item>
                    )}
                  </Autocomplete.Collection>
                </Autocomplete.Group>
              ))}
            </Autocomplete.List>

            <Autocomplete.Empty className="px-4 py-10 text-center text-[13.5px] text-muted-foreground empty:hidden">
              No results for “{query.trim()}”.
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
          </div>
        </DialogPrimitive.Popup>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
