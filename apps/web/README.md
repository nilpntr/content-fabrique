# skeleton-dashboard-ui

A base template for dashboards: React 19, Vite 8, Tailwind 4, shadcn components on Base UI, TanStack Router and Query, and an inset collapsible sidebar shell with light and dark themes.

## Run

```sh
bun install
bun run dev        # http://localhost:5173
bun run check      # typecheck + tests
bun run build
```

## Add a page

1. Create `src/pages/my-page.tsx` exporting a component.
2. Register a route in `src/router.tsx` and add it to `rootRoute.addChildren([...])`.
3. Add the path to the `to` union and an entry in `src/nav.ts`; the sidebar item, active state and header title follow.

## Add shadcn components

`components.json` targets the `base-nova` (Base UI) style:

```sh
bunx shadcn@latest add <component>
```

Theme tokens live in `src/index.css`. The brand colour is `--brand`; `--accent` is the neutral hover surface shadcn expects. The shadcn registry may emit `import ... from "cn"`; change it to `@/lib/utils`.

## Look and motion

Tokens live in `src/index.css`: surfaces and shadows (`--shadow-raised`, `--shadow-pop`, `--shadow-well`), the sidebar lens (`--lens-*`), and motion (`--motion-spring`, `--motion-out`, `--dur-*`). Tailwind exposes them as `ease-spring`, `ease-out` and `shadow-*`; scripts read them with `motion()` from `src/lib/glide.ts`.

- The current page is a lens that springs from item to item. Every `SidebarMenuButton` inside `SidebarContent` takes part: pass `isActive` and put the text in a `SidebarMenuLabel`. Hover and keyboard focus glide on their own.
- Menus and lists get the same travelling highlight from `HighlightGlider`, placed first inside a `relative isolate` container.
- The lens, gliders and popups animate only `transform` and `opacity`; the sidebar collapse still animates width. With `prefers-reduced-motion`, everything snaps.

The user in the sidebar footer (`src/components/nav-user.tsx`) and its menu actions are placeholders to wire up.
