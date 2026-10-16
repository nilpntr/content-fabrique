import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { mergeConfig } from "vite";
import { baseConfig } from "./vite.base";

// Vitest has its own config (vitest.config.ts), so the Start and Cloudflare plugins stay out of tests.
// Start's plugins go first because React's plugin (in baseConfig) must come after Start's.
export default mergeConfig(
  { plugins: [cloudflare({ viteEnvironment: { name: "ssr" } }), tanstackStart()] },
  mergeConfig(baseConfig, {
    plugins: [tailwindcss()],
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
  }),
);
