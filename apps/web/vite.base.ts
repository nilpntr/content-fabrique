import path from "node:path";
import react from "@vitejs/plugin-react";
import type { UserConfig } from "vite";

/** What the app build (vite.config.ts) and the tests (vitest.config.ts) share. */
export const baseConfig: UserConfig = {
  plugins: [react()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
};
