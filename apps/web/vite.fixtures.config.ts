import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

/**
 * The fixtures viewer (`bun run fixtures`): the real app on canned API answers, with no Clerk, for looking at screens and taking
 * screenshots without a backend. It is a second Vite app on its own port. It is NOT part of the product: vite.config.ts (the
 * build) does not know it, nothing under src/ imports it, and `fixtures-isolation.test.ts` fails if that ever changes.
 */
const web = import.meta.dirname;
const src = path.resolve(web, "src");
const stubs = path.resolve(web, "dev-fixtures/stubs");

export default defineConfig({
  root: web,
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: [
      // Clerk, the session and the Start server functions have no server here: the same stand-ins the tests use.
      { find: "@clerk/tanstack-react-start", replacement: path.join(src, "test-clerk.tsx") },
      { find: "@/lib/session", replacement: path.join(stubs, "session.ts") },
      { find: "@/lib/sidebar-open", replacement: path.join(stubs, "sidebar-open.ts") },
      { find: "@/lib/app-origin", replacement: path.join(stubs, "app-origin.ts") },
      { find: "@", replacement: src },
    ],
  },
  define: { "import.meta.env.VITE_API_BASE_URL": JSON.stringify("http://api.fixtures") },
  // Not 3000, 8080, 8081 or 8082: those are the real stack's.
  server: { port: 3100, strictPort: true, fs: { allow: [path.resolve(web, "../.."), "/tmp"] } },
});
