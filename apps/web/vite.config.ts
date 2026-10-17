import path from "node:path";
import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { defineConfig, loadEnv, mergeConfig } from "vite";
import { baseConfig } from "./vite.base";

// The repository's one .env lives two levels up. Vite reads it for the VITE_ variables (the only ones the browser gets).
const ENV_DIR = path.resolve(import.meta.dirname, "../..");

// Vitest has its own config (vitest.config.ts), so the Start and Cloudflare plugins stay out of tests.
// Start's plugins go first because React's plugin (in baseConfig) must come after Start's.
export default defineConfig(({ command, mode }) => {
  // Secrets the Worker needs on the server side only. In production they are Worker secrets (`wrangler secret put`,
  // P0-09). In `vite dev` the Worker runs in a local runtime that does not see the shell's environment, so the ones it
  // needs are handed over here, from the process environment or the root .env. Never for a build: `vars` end up in the
  // generated Worker config in dist/, and a secret does not belong there.
  const env = loadEnv(mode, ENV_DIR, "");
  const devVars: Record<string, string> = {};
  if (command === "serve" && env.CLERK_SECRET_KEY) devVars.CLERK_SECRET_KEY = env.CLERK_SECRET_KEY;

  return mergeConfig(
    { plugins: [cloudflare({ viteEnvironment: { name: "ssr" }, config: { vars: devVars } }), tanstackStart()] },
    mergeConfig(baseConfig, {
      plugins: [tailwindcss()],
      envDir: ENV_DIR,
      // Matches APP_BASE_URL in .env.example. strictPort fails loudly instead of moving to another port.
      server: { port: 3000, strictPort: true },
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
    }),
  );
});
