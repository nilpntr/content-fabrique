import path from "node:path";
import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Vitest has its own config (vitest.config.ts), so the Start and Cloudflare plugins stay out of tests.
export default defineConfig({
  plugins: [
    cloudflare({ viteEnvironment: { name: "ssr" } }),
    tanstackStart(),
    // React's plugin must come after Start's.
    react(),
    tailwindcss(),
  ],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  server: { port: 5173 },
  build: { sourcemap: true },
  environments: {
    client: {
      build: {
        rolldownOptions: {
          output: {
            codeSplitting: {
              groups: [
                { name: "react", test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/ },
                { name: "base-ui", test: /node_modules[\\/]@(base-ui|floating-ui)[\\/]/ },
                { name: "tanstack", test: /node_modules[\\/]@tanstack[\\/]/ },
              ],
            },
          },
        },
      },
    },
  },
});
